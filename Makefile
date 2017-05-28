# Copyright 2015 The Prometheus Authors
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
# http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

GO           := GO15VENDOREXPERIMENT=1 go
FIRST_GOPATH := $(firstword $(subst :, ,$(shell $(GO) env GOPATH)))
PROMU        := $(FIRST_GOPATH)/bin/promu
pkgs          = $(shell $(GO) list ./... | grep -v /vendor/)

PREFIX                  ?= $(shell pwd)
BIN_DIR                 ?= $(shell pwd)
DOCKER_IMAGE_NAME       ?= prometheus
DOCKER_IMAGE_TAG        ?= $(subst /,-,$(shell git rev-parse --abbrev-ref HEAD))

ifdef DEBUG
	bindata_flags = -debug
endif


all: format build test

style:
	@echo ">> checking code style"
	@! gofmt -d $(shell find . -path ./vendor -prune -o -name '*.go' -print) | grep '^'

check_license:
	@echo ">> checking license header"
	@./scripts/check_license.sh

test-short:
	@echo ">> running short tests"
	@$(GO) test -short $(pkgs)

test:
	@echo ">> running all tests"
	@$(GO) test $(pkgs)

format:
	@echo ">> formatting code"
	@$(GO) fmt $(pkgs)

vet:
	@echo ">> vetting code"
	@$(GO) vet $(pkgs)

# 从官方的[README](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/README#L77) 可以得知
# 想要构建得到Prometheus的可执行程序，需要执行`make build`。
# 而`make build`将会进入到这里，构建出`prometheus`和`promtool`两个可执行程序
# build的先置条件是`promu`，因此它会帮你`go get -u github.com/prometheus/promu`
# 尔后，通过promu这个程序来做上述两个目标程序的构建
# 即这些命令最终等同于`$GOPATH/bin/promu build --prefix .`
# promu程序的使用参见: https://github.com/prometheus/promu/blob/master/README.md
# 而promu读取的配置文件即本地仓库的`.promu.yml`
# [当前版本](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/.promu.yml)下的配置内容即：
#
# ```
# build:
#     binaries:
#         - name: prometheus
#           path: ./cmd/prometheus
#         - name: promtool
#           path: ./cmd/promtool
#     flags: -a -tags netgo
#     ldflags: |
#         -X {{repoPath}}/vendor/github.com/prometheus/common/version.Version={{.Version}}
#         -X {{repoPath}}/vendor/github.com/prometheus/common/version.Revision={{.Revision}}
#         -X {{repoPath}}/vendor/github.com/prometheus/common/version.Branch={{.Branch}}
#         -X {{repoPath}}/vendor/github.com/prometheus/common/version.BuildUser={{user}}@{{host}}
#         -X {{repoPath}}/vendor/github.com/prometheus/common/version.BuildDate={{date "20060102-15:04:05"}}
# ```
# 因此，如上述结果那样，会产出两个binary，这样一来，我们也就知道，prom的入口程序在`[cmd](https://github.com/SaltedMan/prometheus-annotated/blob/v1.6.3-annotated/cmd/prometheus/main.go)`!
build: promu
	@echo ">> building binaries"
	@$(PROMU) build --prefix $(PREFIX)

tarball: promu
	@echo ">> building release tarball"
	@$(PROMU) tarball --prefix $(PREFIX) $(BIN_DIR)

docker:
	@echo ">> building docker image"
	@docker build -t "$(DOCKER_IMAGE_NAME):$(DOCKER_IMAGE_TAG)" .

assets:
	@echo ">> writing assets"
	@$(GO) get -u github.com/jteeuwen/go-bindata/...
	@go-bindata $(bindata_flags) -pkg ui -o web/ui/bindata.go -ignore '(.*\.map|bootstrap\.js|bootstrap-theme\.css|bootstrap\.css)'  web/ui/templates/... web/ui/static/...
	@$(GO) fmt ./web/ui

promu:
	@echo ">> fetching promu"
	@GOOS=$(shell uname -s | tr A-Z a-z) \
	GOARCH=$(subst x86_64,amd64,$(patsubst i%86,386,$(shell uname -m))) \
	$(GO) get -u github.com/prometheus/promu


.PHONY: all style check_license format build test vet assets tarball docker promu
