# Divergence Engine

A distributed market data analytics system that ingests real-time crypto trade feeds, detects statistical anomalies via a pluggable detector framework, and scales horizontally across Docker containers.

## Architecture

```
┌─────────────┐   ┌──────────────┐
│ Binance     │   │ Coinbase     │
│ ingester    │   │ ingester     │
└──────┬──────┘   └──────┬───────┘
       └────────┬─────────┘
                ▼
          ┌───────────┐
          │   Redis   │
          │  Streams  │
          └─────┬─────┘
                ▼
   ┌─ detector worker 1 ─┐
   ├─ detector worker 2 ─┼──▶ alerts stream ──▶ aggregator ──▶ Grafana / stdout
   └─ detector worker 3 ─┘
```

**Detectors:** Volume Spike · Order Flow Imbalance · Correlation Breakdown

## Quickstart

```bash
docker compose up
```

## Makefile targets

| Target  | Description                          |
|---------|--------------------------------------|
| `build` | Compile all `cmd/*` binaries         |
| `test`  | Run all unit tests with race detector|
| `up`    | Start all services via Docker Compose|
| `down`  | Stop all services                    |
| `logs`  | Tail compose logs                    |
| `bench` | Run benchmarks → `bench/results/`    |
| `vet`   | Run `go vet`                         |
| `lint`  | Run `golangci-lint`                  |

## Design decisions

- **Redis Streams + consumer groups** — built-in fan-out, per-consumer acknowledgement, and automatic redelivery on worker failure. No custom scheduler needed.
- **Welford's online algorithm** — numerically stable incremental variance/covariance for the correlation detector. O(1) per tick, no recomputation over the window.
- **Sharded concurrent map** — reduces lock contention for per-symbol state across goroutines.
- **Multi-stage Docker builds** — Go compile stage + scratch/distroless runtime; final images ≈15 MB.

## What I'd do next

- Add a persistent alert store (PostgreSQL) for historical querying.
- Integrate a proper message schema with Protobuf for faster ser/des.
- Add a UI dashboard beyond raw Grafana.
- Support equities via Alpaca WebSocket (same architecture, different ingester).
