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

# Acceptance tests against a live lab switch (internal/acctest). Needs the
# FASTIRON_* connection variables plus TF_ACC_SCRATCH_PORT; see the
# "Acceptance tests" section of README.md for the full environment, the
# pre-flight checklist and what the tests touch. -count=1 so a live run is
# never served from the test cache. Narrow with TESTARGS='-run TestAccVLAN'.
testacc:
	TF_ACC=1 go test ./internal/acctest/... -v -count=1 -timeout 120m $(TESTARGS)

lint:
	golangci-lint run ./...

fmt:
	gofmt -s -w .

clean:
	rm -f terraform-provider-fastiron-icx

.PHONY: default build install test testacc lint fmt clean
