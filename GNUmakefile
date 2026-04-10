default: build

build:
	go build -o terraform-provider-fastiron-icx

install: build
	mkdir -p ~/.terraform.d/plugins/registry.terraform.io/pgehres/fastiron-icx/0.1.0/$$(go env GOOS)_$$(go env GOARCH)
	cp terraform-provider-fastiron-icx ~/.terraform.d/plugins/registry.terraform.io/pgehres/fastiron-icx/0.1.0/$$(go env GOOS)_$$(go env GOARCH)/

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
