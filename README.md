# Divergence Engine

Real-time crypto market analytics built in Go. Pulls live trade feeds from Binance over WebSocket, runs statistical anomaly detectors across the stream, and surfaces alerts through a web dashboard. The whole thing runs in Docker and scales horizontally by adding more detector containers.

## Stack

- **Go** — all services (ingester, detector workers, aggregator, dashboard)
- **Redis Streams** — message bus between services
- **Docker / Docker Compose** — container orchestration
- **Binance WebSocket API** — live trade feed (`wss://stream.binance.com`)
- **Server-Sent Events** — real-time browser updates from the dashboard
- **gorilla/websocket**, **go-redis/v9** — only two external dependencies

## Architecture

```
┌──────────────────┐
│ Binance ingester │   (add an exchange: implement ExchangeAdapter, add a compose service)
└────────┬─────────┘
         ▼
   ┌───────────┐
   │   Redis   │
   │  Streams  │
   └─────┬─────┘
         ▼
┌─ detector worker 1 ─┐
├─ detector worker 2 ─┼──▶ alerts stream ──▶ aggregator ──▶ dashboard / stdout
└─ detector worker 3 ─┘
```

**Detectors:** Volume Spike · Order Flow Imbalance · Correlation Breakdown

## Quickstart

```bash
docker compose up
# dashboard at http://localhost:8080
```

## Makefile targets

| Target           | Description                                        |
|------------------|----------------------------------------------------|
| `build`          | Compile all `cmd/*` binaries into `./bin/`         |
| `test`           | Run unit tests with the race detector              |
| `up`             | Start all services                                 |
| `down`           | Stop all services                                  |
| `logs`           | Tail compose logs                                  |
| `bench`          | Run unit benchmarks                                |
| `bench-scale`    | Load test at 5000/s while scaling detector workers |
| `bench-recovery` | Kill a worker mid-run and watch the group recover  |
| `vet`            | `go vet`                                           |
| `lint`           | `golangci-lint`                                    |

## Performance

Benchmarked on a laptop with a single detector container:

- 3000 ticks/s sustained across BTC/ETH/SOL feeds
- 1 to 3ms end-to-end lag (tick published → alert visible)
- Zero queue buildup over a 90s run
- Pending never exceeded 1 (the current in-flight batch)

Adding more detector workers splits the stream automatically via the Redis consumer group. No config changes needed, just `docker compose up --scale detector=N`.

## Design decisions

**Redis Streams with consumer groups** — each worker pulls its own slice of the stream and ACKs messages after processing. If a worker dies, Redis redelivers its unacked messages to the next worker that claims them. No custom retry logic.

**Welford's online algorithm** — the correlation detector computes incremental variance and covariance in O(1) per tick without storing the full window. Numerically stable even over long runs.

**Sharded concurrent map** — symbol state is spread across 16 shards with independent locks. Hot symbols don't block each other.

**Single Dockerfile, four binaries** — one multi-stage build parameterized by `ARG CMD`. Final images are around 15 MB.

**Pluggable detectors** — implement the `Detector` interface (`Name() string`, `OnTick(...) []Alert`) and register it. The worker scaffolding, Redis plumbing, and alert routing don't change.

## What's next

- Persistent alert store in PostgreSQL for historical queries
- Protobuf for faster serialization on the hot path
- Equities support via Alpaca WebSocket (same architecture, different ingester)
