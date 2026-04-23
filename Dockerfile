ARG CMD=ingester-binance

FROM golang:1.26-alpine AS builder
ARG CMD
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -ldflags="-s -w" -o /service ./cmd/${CMD}

FROM alpine:3.20
# ca-certificates: required for TLS (wss:// WebSocket connections)
# tzdata: required for correct UTC time handling
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /service /service
ENTRYPOINT ["/service"]
