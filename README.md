# Divergence Engine

This is a real-time crypto market analytics system built in Go. It pulls live trade data from Binance using WebSockets, runs various statistical detectors to spot anomalies like volume spikes, order flow imbalances, and correlation breakdowns, then sends alerts through a web dashboard. Everything runs in Docker and can scale by adding more detector workers.

## What It Does

The engine handles a bunch of tasks to keep an eye on crypto markets:

- **Data Ingestion**: Connects to Binance's WebSocket API to grab real-time trade ticks for symbols like BTC-USDT, ETH-USDT, and SOL-USDT. It handles reconnections automatically if the connection drops.

- **Detection**: Runs multiple detectors on the incoming data stream:
  - Volume Spike: Spots sudden jumps in trading volume that might signal big moves.
  - Order Flow Imbalance: Looks for imbalances between buy and sell orders that could indicate market pressure.
  - Correlation Breakdown: Tracks how different assets correlate and flags when those relationships break down unexpectedly.

- **Alerting**: When a detector finds something, it sends an alert. The system aggregates these, removes duplicates within a minute, and pushes them out.

- **Dashboard**: A simple web interface shows alerts in real-time using Server-Sent Events. It also lets you run backtests on historical data and simulate live trading strategies with fake money.

- **Backtesting Engine**: Evaluates strategies on historical Binance candle data using SMA/EMA crossovers, RSI mean reversion, Bollinger Bands, and breakout rules. It returns signal lists, win rate, forward returns, and strategy performance metrics.

- **Live Strategy Simulator**: Runs a stateful paper-trading session against recent Binance data. It executes buy/sell decisions, tracks PnL, trade count, and total account value over time.

- **Benchmarking**: Scripts to measure performance, like how fast alerts show up and if the system scales when you add more workers.

- **Scaling**: Built on Redis Streams with consumer groups, so adding more detector containers splits the work automatically. No extra config needed.

## Tech Stack

- Go for all the services (ingester, detectors, aggregator, dashboard)
- Redis Streams as the message bus between components
- Docker and Docker Compose for running everything
- Binance WebSocket API for live data
- Server-Sent Events for real-time updates in the browser
- Just two external libraries: gorilla/websocket and go-redis/v9

## Architecture

This is the key data flow for the engine:

```
+-----------------+        +----------------+        +---------------------+
| Binance WebSocket| -----> | Binance Ingester| -----> | Redis Streams (tick) |
+-----------------+        +----------------+        +----------+----------+
                                                                 |
                                                                 v
                                                       +---------------------+
                                                       | Detector Workers     |
                                                       | (volume / orderflow |
                                                       |  / correlation)     |
                                                       +----------+----------+
                                                                 |
                                                                 v
                                         +----------------+   +--------------------+
                                         | Alerts Stream  |<--| Aggregator /       |
                                         | (cleaned, de-  |   | Deduper           |
                                         | duplicated)    |   +--------------------+
                                         +----------------+                |
                                                                                  v
                                                                          +----------------+
                                                                          | Dashboard      |
                                                                          | + SSE live UI  |
                                                                          | + Backtest API  |
                                                                          | + Live strategy |
                                                                          +----------------+
```

- The ingester receives live trade ticks from Binance and writes them into Redis Streams.
- Detector workers pull from the tick stream, run their anomalies and signal checks, then publish alerts.
- The aggregator reads alert events, removes duplicates, and forwards clean alerts into the dashboard stream.
- The dashboard consumes live alerts and also exposes backtest and live strategy APIs.

Detectors are pluggable: you can add new ones by implementing a simple interface.

## Getting Started

Make sure you have Docker and Docker Compose installed. Then:

```bash
docker compose up
```

Open http://localhost:8080 in your browser for the dashboard.

To build from source:

```bash
make build  # compiles all binaries to ./bin/
make test   # runs tests
make up     # starts services
```

## Usage

- **Ingester**: Pulls from Binance. Set SYMBOLS env var for different pairs, like SYMBOLS=BTC-USDT,ETH-USDT.

- **Detectors**: Scale them by adding more detector service replicas in Docker Compose. They share the work via Redis groups.

- **Dashboard**: 
  - View live alerts.
  - Backtest strategies on historical Binance data via `/api/backtest`.
  - Start and stop live strategy simulations with `/api/live-strategy/start` and `/api/live-strategy/stop`.
  - Monitor paper-trading sessions, including PnL, total value, and current signal.

- **Load Generator**: Run go run ./bench/loadgen -rate 1000 -duration 60s to simulate traffic.

- **Benchmarks**: Use make bench-scale to test scaling, or make bench-recovery to see how it handles failures.

## Backtesting and Live Strategy

- The dashboard can fetch historical Binance candle data and simulate strategy performance over a date range.
- Supported backtest strategies include:
  - SMA crossover
  - EMA crossover
  - RSI mean reversion
  - Bollinger Bands
  - Breakout
- Backtests return signal timestamps, forward returns, win rate, correlation, and total strategy return.
- Live strategy simulation is a paper-trading engine that polls recent Binance closes, generates buy/sell/hold signals, and tracks a notional account balance.
- Live sessions start/stop per symbol and broadcast updates to the dashboard with current price, PnL, trade count, and position state.

## Performance

On a standard laptop with one detector:

- Handles 3000 ticks per second across multiple symbols.
- Alerts show up in 1-3 milliseconds from tick to dashboard.
- No backlog buildup over long runs.
- Scales linearly by adding workers.

## Design Choices

- Redis Streams with consumer groups for reliable message handling. Workers ACK messages, and Redis retries if one dies.
- Online algorithms for stats, like Welford's for correlation, so it doesn't store huge histories.
- Sharded maps for state to avoid locks blocking everything.
- One Dockerfile builds all binaries, keeping images small (15MB).
- Minimal dependencies to keep things simple and fast.

## Future Plans

- Add a database for storing alerts history.
- Switch to Protobuf for quicker data transfer.
- Support more exchanges like Alpaca for stocks.
- More detectors and strategies.
