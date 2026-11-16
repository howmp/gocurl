BINARY ?= gocurl
GO ?= go

.PHONY: all build test vet clean

all: build

build:
	$(GO) build -trimpath -o $(BINARY) ./cmd/gocurl

test:
	$(GO) test ./...

vet:
	$(GO) vet ./...

clean:
	rm -f $(BINARY)
