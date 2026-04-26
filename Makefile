SHELL := /bin/bash
MODULE := github.com/youssef/divergence-engine

.PHONY: build test up down logs bench bench-scale bench-recovery lint vet

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

## bench: run unit benchmarks and save results to bench/results/
bench:
	@mkdir -p bench/results
	go test -bench=. -benchmem -count=3 ./internal/... | tee bench/results/latest.txt

## bench-scale: Demo 1 — scale from 1 to 4 detector workers while loadgen runs at 5000 ticks/s.
##   Shows near-linear throughput increase as workers are added.
##   Prerequisites: stack is running (make up). Run loadgen in background, then scale.
bench-scale:
	@mkdir -p bench/results
	@echo "=== bench-scale: starting loadgen at 5000/s for 90s ==="
	go run ./bench/loadgen -rate 5000 -duration 90s | tee bench/results/scale.txt &
	@LOADGEN_PID=$$!; \
	echo "Workers=1 (baseline for 20s)..."; sleep 20; \
	echo "Scaling to 2 workers..."; docker compose up -d --scale detector=2 --no-recreate; sleep 20; \
	echo "Scaling to 3 workers..."; docker compose up -d --scale detector=3 --no-recreate; sleep 20; \
	echo "Scaling to 4 workers..."; docker compose up -d --scale detector=4 --no-recreate; sleep 30; \
	wait $$LOADGEN_PID; \
	echo "Restoring to 1 worker..."; docker compose up -d --scale detector=1 --no-recreate
	@echo "Results saved to bench/results/scale.txt"

## bench-recovery: Demo 2 — kill one detector worker mid-run and watch the group recover.
##   Loadgen runs at 300/s for 90s; worker is killed at t=30s, a new one starts at t=50s.
##   Prerequisites: stack is running (make up).
bench-recovery:
	@mkdir -p bench/results
	@echo "=== bench-recovery: starting loadgen at 300/s for 90s ==="
	go run ./bench/loadgen -rate 300 -duration 90s | tee bench/results/recovery.txt &
	@LOADGEN_PID=$$!; \
	echo "Running normally for 30s..."; sleep 30; \
	echo "KILLING one detector worker..."; \
	VICTIM=$$(docker compose ps -q detector | head -1); \
	docker kill $$VICTIM; \
	echo "Worker killed. Pending messages will accumulate for 20s..."; sleep 20; \
	echo "Restarting worker..."; docker compose up -d --scale detector=1 --no-recreate; \
	echo "Recovery in progress for 40s..."; sleep 40; \
	wait $$LOADGEN_PID
	@echo "Results saved to bench/results/recovery.txt"
