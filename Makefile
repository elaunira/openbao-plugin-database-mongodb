.PHONY: build clean test lint fmt vet

BINARY_NAME=mongodb-database-plugin
VERSION?=dev
LDFLAGS=-ldflags "-X main.version=$(VERSION)"

build:
	CGO_ENABLED=0 go build $(LDFLAGS) -o $(BINARY_NAME) ./cmd/$(BINARY_NAME)

build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(LDFLAGS) -o $(BINARY_NAME)-linux-amd64 ./cmd/$(BINARY_NAME)
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(LDFLAGS) -o $(BINARY_NAME)-linux-arm64 ./cmd/$(BINARY_NAME)

clean:
	rm -f $(BINARY_NAME) $(BINARY_NAME)-*

test:
	go test -v -race -cover ./...

test-short:
	go test -v -short ./...

lint:
	golangci-lint run

fmt:
	go fmt ./...

vet:
	go vet ./...

tidy:
	go mod tidy

sha256:
	@sha256sum $(BINARY_NAME) | cut -d' ' -f1

install: build
	mkdir -p $(DESTDIR)/usr/lib/openbao/plugins
	install -m 755 $(BINARY_NAME) $(DESTDIR)/usr/lib/openbao/plugins/

.DEFAULT_GOAL := build
