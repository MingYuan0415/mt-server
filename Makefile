.PHONY: all build check clean docker-build format format-check test test-race vet web-test

GO ?= go
BINARY := bin/mt-server

all: check build

format:
	$(GO) fmt ./...

format-check:
	@test -z "$$(gofmt -l .)" || { gofmt -l .; exit 1; }

vet:
	$(GO) vet ./...

test:
	$(GO) test -coverprofile=coverage.out ./...

test-race:
	$(GO) test -race ./...

check: format-check vet test test-race

build:
	mkdir -p bin
	CGO_ENABLED=0 $(GO) build -trimpath -o $(BINARY) ./cmd/mt-server

docker-build:
	docker build -t mt-server:dev .

web-test:
	npm ci
	npx playwright test

clean:
	rm -f $(BINARY) coverage.out
