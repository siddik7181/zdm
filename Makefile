.PHONY: help build test test-race cover lint fmt vet tidy check lab-up lab-down

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
	@echo "lab-up     start the local MySQL lab (when docker/ exists)"
	@echo "lab-down   stop it"

build:
	go build -o bin/zdm ./cmd/zdm

test:
	go test ./...

test-race:
	go test -race ./...

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

lab-down:
	docker compose -f docker/compose.yml down -v
