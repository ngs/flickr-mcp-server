.PHONY: build test generate lint fmt check

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
