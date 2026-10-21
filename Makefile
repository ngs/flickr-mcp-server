.PHONY: build test generate lint fmt check e2e

BINARY_NAME=flickr-mcp-server

build:
	go build -o $(BINARY_NAME) .

test:
	go test -race ./...

generate:
	go generate ./...

lint:
	golangci-lint run ./...

fmt:
	gofmt -w .

# Everything CI runs, in the same order.
check:
	test -z "$$(gofmt -l .)"
	go generate ./... && git diff --exit-code
	go vet ./...
	go build ./...
	go test -race ./...
	golangci-lint run ./...

# Calls read-only tools against the real Flickr API with your credentials.
e2e:
	FLICKR_E2E=1 go test -run TestE2EReadOnly -v -count=1 .
