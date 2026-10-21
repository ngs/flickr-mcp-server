.PHONY: build test generate lint fmt check e2e set-version release-snapshot

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

# Usage: make set-version VERSION=v0.1.0
set-version:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make set-version VERSION=v0.1.0"; exit 1; fi
	@v=$$(echo "$(VERSION)" | sed 's/^v//'); \
	sed -i.bak "s/^const VERSION = \".*\"$$/const VERSION = \"$$v\"/" server/version.go && rm server/version.go.bak; \
	echo "server/version.go is now $$v. Commit it on main before tagging v$$v."

# Builds every release archive locally without publishing anything.
release-snapshot:
	go run github.com/goreleaser/goreleaser/v2@latest release --snapshot --clean
