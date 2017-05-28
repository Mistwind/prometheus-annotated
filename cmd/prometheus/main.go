// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// The main package for the Prometheus server executable.
package main

import (
	"flag"
	"fmt"
	_ "net/http/pprof" // Comment this line to disable pprof endpoint.
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/log"
	"github.com/prometheus/common/version"
	"golang.org/x/net/context"

	"github.com/prometheus/prometheus/config"
	"github.com/prometheus/prometheus/notifier"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/retrieval"
	"github.com/prometheus/prometheus/rules"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/storage/fanin"
	"github.com/prometheus/prometheus/storage/local"
	"github.com/prometheus/prometheus/storage/remote"
	"github.com/prometheus/prometheus/web"
)

func main() {
	// 使用os.Exit让Prometheus程序完成退出
	// 退出码即Main函数的返回值, 0则安全退出
	// 详细参考：https://golang.org/pkg/os/#Exit
	os.Exit(Main())
}

// defaultGCPercent is the value used to to call SetGCPercent if the GOGC
// environment variable is not set or empty. The value here is intended to hit
// the sweet spot between memory utilization and GC effort. It is lower than the
// usual default of 100 as a lot of the heap in Prometheus is used to cache
// memory chunks, which have a lifetime of hours if not days or weeks.
// 参考：https://golang.org/pkg/runtime/debug/#SetGCPercent
const defaultGCPercent = 40

var (
	configSuccess = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "prometheus",
		Name:      "config_last_reload_successful",
		Help:      "Whether the last configuration reload attempt was successful.",
	})
	configSuccessTime = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "prometheus",
		Name:      "config_last_reload_success_timestamp_seconds",
		Help:      "Timestamp of the last successful configuration reload.",
	})
)

func init() {
	prometheus.MustRegister(version.NewCollector("prometheus"))
}

// Main manages the startup and shutdown lifecycle of the entire Prometheus server.
func Main() int {
	// 使用当前包里config.go的[parse](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/cmd/prometheus/config.go#L272)
	// 解析启动命令传入的参数，包括promql的查询引擎、所选的存储引擎、alertmanager指向、配置文件路径等等；
	if err := parse(os.Args[1:]); err != nil {
		log.Error(err)
		return 2
	}

	if cfg.printVersion {
		fmt.Fprintln(os.Stdout, version.Print("prometheus"))
		return 0
	}

	if os.Getenv("GOGC") == "" {
		debug.SetGCPercent(defaultGCPercent)
	}

	log.Infoln("Starting prometheus", version.Info())
	log.Infoln("Build context", version.BuildContext())

	var (
		// 采样数据添加器，将采集到的数据发送到列表里的每个采样器
		// 采样器主要负责数据采集逻辑后面的Append和Throttling处理
		// sampleAppender结构体的定义：https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/storage/storage.go#L22
		sampleAppender = storage.Fanout{}
		// 重载管理，在新配置出现并发起reload时触发一系列组件的reload响应
		reloadables []Reloadable
	)

	// 本地存储引擎的抽象定义，实现采集和管理样本数据、启停、索引和删除等操作
	// 参见: https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/storage/local/interface.go#L28
	var localStorage local.Storage
	switch cfg.localStorageEngine {
	// 实例化本地存储引擎和采样器，`cfg.storage`为传入的一组[存储参数](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/storage/local/storage.go#L188)
	// 包括target heap size，retention policy等等
	// 当前本地引擎仅支持[MemorySeriesStorage](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/storage/local/storage.go#L135)
	// 如果本地存储引擎参数为`none`，Prometheus将仅会向remote storage发送采样得到的数据
	case "persisted":
		localStorage = local.NewMemorySeriesStorage(&cfg.storage)
		sampleAppender = storage.Fanout{localStorage}
	case "none":
		localStorage = &local.NoopStorage{}
	default:
		log.Errorf("Invalid local storage engine %q", cfg.localStorageEngine)
		return 1
	}

	// 配置远程读\写器，并将写加入到采样数据添加器里，然后将远程读\写器加入到reloadable对象里
	// TODO: 搞清楚为什么sampleAppender不需要加入到reloadables里?
	remoteAppender := &remote.Writer{}
	sampleAppender = append(sampleAppender, remoteAppender)
	remoteReader := &remote.Reader{}
	reloadables = append(reloadables, remoteAppender, remoteReader)

	// 实例化queryable对象，它将从localStorage或Remote读取数据
	// `fanin`"刷入"包实现了对数据查询的封装
	queryable := fanin.Queryable{
		Local:  localStorage,
		Remote: remoteReader,
	}

	var (
		// [notifier](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/notifier/notifier.go#L55)
		// 根据alertRules分析出的告警事件，将其发送给alertmanager
		notifier = notifier.New(&cfg.notifier)
		// targetManager管理对target的抓取和实际执行，并将这些数据发送给采样器
		targetManager = retrieval.NewTargetManager(sampleAppender)
		// 查询引擎的初始化，它管理对queryable提供的远程读/本地存储的调用
		queryEngine = promql.NewEngine(queryable, &cfg.queryEngine)
		// TODO: 搞清楚ctx的用途
		ctx, cancelCtx = context.WithCancel(context.Background())
	)

	// ruleManager负责对alertRules和recordingRules这块功能的执行
	ruleManager := rules.NewManager(&rules.ManagerOptions{
		SampleAppender: sampleAppender,
		Notifier:       notifier,
		QueryEngine:    queryEngine,
		Context:        fanin.WithLocalOnly(ctx),
		// 重载alertmanager的url为指定的外部链接，解决alertmanager本身部署在反向代理后面的访问情况
		// 参见：https://github.com/prometheus/alertmanager/issues/95
		ExternalURL: cfg.web.ExternalURL,
	})

	cfg.web.Context = ctx
	cfg.web.Storage = localStorage
	cfg.web.QueryEngine = queryEngine
	cfg.web.TargetManager = targetManager
	cfg.web.RuleManager = ruleManager
	cfg.web.Notifier = notifier

	cfg.web.Version = &web.PrometheusVersion{
		Version:   version.Version,
		Revision:  version.Revision,
		Branch:    version.Branch,
		BuildUser: version.BuildUser,
		BuildDate: version.BuildDate,
		GoVersion: version.GoVersion,
	}

	// 设置web server组件的一些启动参数
	cfg.web.Flags = map[string]string{}
	cfg.fs.VisitAll(func(f *flag.Flag) {
		cfg.web.Flags[f.Name] = f.Value.String()
	})

	// 创建web服务实例
	webHandler := web.New(&cfg.web)

	// 将targetManager、ruleManager、webHandler、notifier也加入到reloadable列表里
	// 这样一来，它们也支持重新热加载新的配置
	reloadables = append(reloadables, targetManager, ruleManager, webHandler, notifier)

	// 第一次启动时同样依靠`reloadConfig`方法来载入配置文件里的参数配置
	if err := reloadConfig(cfg.configFile, reloadables...); err != nil {
		log.Errorf("Error loading config: %s", err)
		return 1
	}

	// Wait for reload or termination signals. Start the handler for SIGHUP as
	// early as possible, but ignore it until we are ready to handle reloading
	// our config.
	hup := make(chan os.Signal)
	hupReady := make(chan bool)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		// 堵塞通道
		// 先启动Main方法内本次goroutine后面的组件
		// 然后等到hupReady再继续
		<-hupReady
		// 该goroutine往复监听reload事件
		for {
			select {
			// hup通道接收到SIGHUP信号或者web服务的`Reload`方法被调用时
			// 重新热加载配置
			// select-case即保证一直阻塞，直到收到某个通道传来的值并执行对应操作
			case <-hup:
				if err := reloadConfig(cfg.configFile, reloadables...); err != nil {
					log.Errorf("Error reloading config: %s", err)
				}
			// 这里是一个黑科技，首先Reload方法会帮助初始化一个rc通道
			// 数据类型是`make(chan chan error)`
			// 当POST请求`/-/reload`时，会调用web的reload方法
			// [reloadCH](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/web/web.go#L409)会收到rc chan然后进入处理过程
			// reload方法[block](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/web/web.go#L410)会向reloadCH（即这里的rc）继续取err类型数据
			// 尔后，当前goroutine开始调用reloadConfig进行reload，拿到err返回值并回传给web handle的err，web处理结束并返回结果，本次reload也正常结束
			case rc := <-webHandler.Reload():
				if err := reloadConfig(cfg.configFile, reloadables...); err != nil {
					log.Errorf("Error reloading config: %s", err)
					rc <- err
				} else {
					rc <- nil
				}
			}
		}
	}()

	// Start all components. The order is NOT arbitrary.

	// [启动本地存储引擎](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/storage/local/storage.go#L383)
	// TODO: 详细分析本地存储引擎的启动过程和内部细节
	if err := localStorage.Start(); err != nil {
		log.Errorln("Error opening memory series storage:", err)
		return 1
	}
	defer func() {
		if err := localStorage.Stop(); err != nil {
			log.Errorln("Error stopping storage:", err)
		}
	}()

	defer remoteAppender.Stop()

	// The storage has to be fully initialized before registering.
	// TODO: 搞清楚这部分...
	if instrumentedStorage, ok := localStorage.(prometheus.Collector); ok {
		prometheus.MustRegister(instrumentedStorage)
	}
	prometheus.MustRegister(configSuccess)
	prometheus.MustRegister(configSuccessTime)

	// The notifier is a dependency of the rule manager. It has to be
	// started before and torn down afterwards.
	go notifier.Run()
	defer notifier.Stop()

	go ruleManager.Run()
	defer ruleManager.Stop()

	go targetManager.Run()
	defer targetManager.Stop()

	// Shutting down the query engine before the rule manager will cause pending queries
	// to be canceled and ensures a quick shutdown of the rule manager.
	// 如原文注释，取消一些滞后的无效查询
	defer cancelCtx()

	// 启动web服务
	go webHandler.Run()

	// Wait for reload or termination signals.
	// 所有组件触发启动结束，告知可以处理reload行为
	// 仍然可能有潜在的风险，比如在webHandler未启动时立马触发了reload
	// 可能导致未定义的行为，这也是把reload goroutine放在前面初始化的原因
	close(hupReady) // Unblock SIGHUP handler.

	term := make(chan os.Signal)
	signal.Notify(term, os.Interrupt, syscall.SIGTERM)
	// 处理关闭行为，包括安全的执行上述一些defer行为，如`localStorage.Stop()`
	select {
	case <-term:
		log.Warn("Received SIGTERM, exiting gracefully...")
	case <-webHandler.Quit():
		log.Warn("Received termination request via web service, exiting gracefully...")
	case err := <-webHandler.ListenError():
		log.Errorln("Error starting web server, exiting gracefully:", err)
	}

	log.Info("See you next time!")
	return 0
}

// Reloadable things can change their internal state to match a new config
// and handle failure gracefully.
type Reloadable interface {
	ApplyConfig(*config.Config) error
}

func reloadConfig(filename string, rls ...Reloadable) (err error) {
	log.Infof("Loading configuration file %s", filename)
	defer func() {
		if err == nil {
			configSuccess.Set(1)
			configSuccessTime.Set(float64(time.Now().Unix()))
		} else {
			configSuccess.Set(0)
		}
	}()

	conf, err := config.LoadFile(filename)
	if err != nil {
		return fmt.Errorf("couldn't load configuration (-config.file=%s): %v", filename, err)
	}

	// Add AlertmanagerConfigs for legacy Alertmanager URL flags.
	for us := range cfg.alertmanagerURLs {
		acfg, err := parseAlertmanagerURLToConfig(us)
		if err != nil {
			return err
		}
		conf.AlertingConfig.AlertmanagerConfigs = append(conf.AlertingConfig.AlertmanagerConfigs, acfg)
	}

	failed := false
	for _, rl := range rls {
		if err := rl.ApplyConfig(conf); err != nil {
			log.Error("Failed to apply configuration: ", err)
			failed = true
		}
	}
	if failed {
		return fmt.Errorf("one or more errors occurred while applying the new configuration (-config.file=%s)", filename)
	}
	return nil
}
