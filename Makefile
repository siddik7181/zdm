.PHONY: tools e2e help build test test-race test-integration cover lint fmt vet tidy check lab-up lab-down

help:
	@echo "build      build ./bin/zdm"
	@echo "test       unit tests"
	@echo "test-race  unit tests with the race detector"
	@echo "cover      coverage report"
	@echo "lint       golangci-lint (includes type checks via govet/staticcheck)"
	@echo "fmt        gofumpt + goimports via golangci-lint"
	@echo "vet        go vet"
	@echo "tidy       go mod tidy"
	@echo "check      fmt-check, vet, lint, test (what CI runs)"
	@echo "lab-up     start + wire the MySQL lab (MYSQL_VERSION=8.4|9.7)"
	@echo "lab-down   stop it"

build:
	go build -o bin/zdm ./cmd/zdm

test:
	go test ./...

test-race:
	go test -race ./...

# Needs the lab running (make lab-up). Run once per MYSQL_VERSION.
test-integration:
	ZDM_TEST_DSN='root:root@tcp(127.0.0.1:33306)/' \
	ZDM_TEST_REPLICA_DSN='root:root@tcp(127.0.0.1:33307)/' \
	go test -tags integration -count=1 ./internal/...

cover:
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

vet:
	go vet ./...

lint:
	golangci-lint run

fmt:
	golangci-lint fmt

tidy:
	go mod tidy

check: vet lint test

lab-up:
	docker compose -f docker/compose.yml up -d
	docker/init-replicas.sh

lab-down:
	docker compose -f docker/compose.yml down -v

# gh-ost into ./bin (pt-online-schema-change comes from percona-toolkit: brew/apt install it).
GHOST_VERSION ?= 1.1.11
tools:
	@mkdir -p bin
	@os=$$(uname -s | tr A-Z a-z); arch=$$(uname -m); case $$arch in x86_64) arch=amd64;; aarch64|arm64) arch=arm64;; esac; \
	curl -fsSL https://github.com/github/gh-ost/releases/download/v$(GHOST_VERSION)/gh-ost-$$os-$$arch.tar.gz | tar -xz -C bin gh-ost

# Whole docker suite on both MySQL versions: lab up, integration tests, lab down. Takes several minutes.
e2e: tools
	@set -e; for v in 8.4 9.7; do \
		echo "== MySQL $$v"; $(MAKE) lab-down >/dev/null 2>&1 || true; $(MAKE) lab-up MYSQL_VERSION=$$v >/dev/null; \
		PATH="$(CURDIR)/bin:$$PATH" $(MAKE) test-integration || { $(MAKE) lab-down >/dev/null 2>&1; exit 1; }; \
	done; $(MAKE) lab-down >/dev/null 2>&1
