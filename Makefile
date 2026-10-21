.PHONY: build test generate lint fmt check e2e set-version release-snapshot coverage

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

# Fails when total statement coverage drops below COVERAGE_MIN percent.
# -coverpkg counts code reached from another package's tests (the stdio server
# is exercised from main's). tools/zz_generated.go is a data table with no
# statements, so it does not count either way.
COVERAGE_MIN ?= 98
coverage:
	go test -race -coverpkg=./... -coverprofile=coverage.txt -covermode=atomic ./...
	@go tool cover -func=coverage.txt | awk -v min=$(COVERAGE_MIN) '/^total:/ { sub("%", "", $$3); printf "total coverage: %s%% (minimum %s%%)\n", $$3, min; if ($$3 + 0 < min) { print "coverage is below the minimum"; exit 1 } }'
