GOCACHE ?= /tmp/polar-go-cache
GO := env GOCACHE=$(GOCACHE) go

.PHONY: build tidy vet test run

build:
	CGO_ENABLED=0 $(GO) build -o bin/dns-svc ./cmd/dns-svc

tidy:
	$(GO) mod tidy

vet:
	$(GO) vet ./...

test:
	$(GO) test ./...

run:
	$(GO) run ./cmd/dns-svc
