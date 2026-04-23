SHELL := /bin/bash
MODULE := github.com/youssef/divergence-engine

.PHONY: build test up down logs bench lint vet

## build: compile all cmd/* binaries into ./bin/
build:
	@mkdir -p bin
	@for dir in cmd/*/; do \
		name=$$(basename $$dir); \
		echo "building $$name..."; \
		go build -o bin/$$name ./$$dir; \
	done

## test: run all unit tests
test:
	go test -race -count=1 ./...

## vet: run go vet
vet:
	go vet ./...

## lint: run golangci-lint (requires golangci-lint installed)
lint:
	golangci-lint run ./...

## up: start all services with docker compose
up:
	docker compose up --build -d

## down: stop all services
down:
	docker compose down

## logs: tail logs from all running services
logs:
	docker compose logs -f

## bench: run benchmarks and save results to bench/results/
bench:
	@mkdir -p bench/results
	go test -bench=. -benchmem -count=3 ./bench/... | tee bench/results/latest.txt
