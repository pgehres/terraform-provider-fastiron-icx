# Version installed into the local plugin directory. This was hardcoded to 0.1.0,
# which meant every `make install` overwrote the 0.1.0 binary no matter which commit
# was built — so a workspace pinned to 0.1.0 could silently be running any code, and
# a newly tagged version could never be installed alongside it. Override per build:
#   make install VERSION=0.1.1
VERSION ?= 0.1.1
PLUGIN_DIR = ~/.terraform.d/plugins/registry.terraform.io/pgehres/fastiron-icx

default: build

build:
	go build -ldflags "-X main.version=$(VERSION)" -o terraform-provider-fastiron-icx

install: build
	mkdir -p $(PLUGIN_DIR)/$(VERSION)/$$(go env GOOS)_$$(go env GOARCH)
	cp terraform-provider-fastiron-icx $(PLUGIN_DIR)/$(VERSION)/$$(go env GOOS)_$$(go env GOARCH)/

test:
	go test ./... -v

testacc:
	TF_ACC=1 go test ./... -v -timeout 120m

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

clean:
	rm -f terraform-provider-fastiron-icx

.PHONY: default build install test testacc lint fmt clean
