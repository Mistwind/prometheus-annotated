# Prometheus 源码注释

本项目是[Prometheus](https://github.com/prometheus/prometheus)的fork项目，灵感源自于huangz1990同学的[redis3.0源码注释](https://github.com/huangz1990/redis-3.0-annotated)项目，旨在通过添加一些代码的注释，给更多的人提供一些学习和深入Prometheus监控系统的资料。

## PR要求

本次注释是基于[Prometheus v1.6.3](https://github.com/SaltedMan/prometheus-annotated/commit/c580b60c67f2c5f6b638c3322161bcdf6d68d7fc)的源码，对应切出的分支是`v1.6.3-annotated`，**不允许任何源码内容方面的改动**，需要链接介绍或详细解释等博客文章，请在注释里添加对应的链接，并在本README里PR内容链接。

## 源码结构

源文件 | 用途 |
---- | ------ |
[Makefile build](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/Makefile#L55) | 编译入口，产出可执行文件`prometheus`和`promtool` |
[Prometheus cmd.prometheus.main](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/cmd/prometheus/main.go#L73) | `prometheus`启动入口 |

## 相关文章和参考

\[1\] [Prometheus 实战于源码分析之服务启动
](http://blog.h5min.cn/u010278923/article/details/70912256), 柳清风的专栏, CSDN, 2017.04.28

## 结语

最后，期待更多的源码解读和开源贡献，为互联网的分享和自由精神干杯!

Colstuwjx

2017.05.28
