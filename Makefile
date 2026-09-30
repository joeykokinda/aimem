BINARY := bin/aimem
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test fmt vet install clean

build:
	go build -ldflags "-X main.Version=$(VERSION)" -o $(BINARY) ./cmd/aimem

test:
	./test.sh

fmt:
	gofmt -w .

vet:
	go vet ./...

install:
	./install.sh

clean:
	rm -rf bin dist
