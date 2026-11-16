BINARY ?= gocurl
GO := go

ifeq ($(strip $(BINARY)),)
$(error BINARY must not be empty)
endif
ifneq ($(words $(BINARY)),1)
$(error BINARY must be a single file name)
endif
ifneq ($(findstring /,$(BINARY)),)
$(error BINARY must not contain path separators)
endif
ifneq ($(findstring *,$(BINARY)),)
$(error BINARY must not contain glob characters)
endif
ifneq ($(findstring ?,$(BINARY)),)
$(error BINARY must not contain glob characters)
endif
ifneq ($(findstring [,$(BINARY)),)
$(error BINARY must not contain glob characters)
endif
ifneq ($(findstring ],$(BINARY)),)
$(error BINARY must not contain glob characters)
endif
ifneq ($(filter -%,$(BINARY)),)
$(error BINARY must not start with '-')
endif

.PHONY: all build test vet clean

all: build

build:
	"$(GO)" build -trimpath -o "$(BINARY)" ./cmd/gocurl

test:
	"$(GO)" test ./...

vet:
	"$(GO)" vet ./...

clean:
	rm -f -- "$(BINARY)"
