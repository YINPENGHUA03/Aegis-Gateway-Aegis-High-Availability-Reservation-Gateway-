# Aegis Gateway

[English](README.md) | [简体中文](README.zh.md)

> High-concurrency scarce-resource reservation system: Redis Lua + RabbitMQ dead letter queue + **reliable-message eventual consistency**. 50K QPS on a single host.

[![Go Version](https://img.shields.io/badge/Go-1.26-00ADD8)]()
[![Redis](https://img.shields.io/badge/Redis-7.0-DC382D)]()
[![RabbitMQ](https://img.shields.io/badge/RabbitMQ-3.12-FF6600)]()

---

## Background

Solves the classic scarce-resource flash-sale problem (vaccine appointments, limited drops, ticketing): preventing overselling, blocking duplicate submissions per user, and automatically cancelling unpaid orders after a timeout.

---

## Architecture

```mermaid
graph TD
    Client([Client]) -->|"POST /reserve"| IP

    subgraph Aegis["Aegis Gateway"]
        direction TB
        IP["IP Rate Limit"] --> AntiSpam["AntiSpam (per-user distributed lock)"]
        AntiSpam --> Lua["Lua Atomic Deduct (Redis)"]
        Lua --> Publish["Publish (RabbitMQ)"]

        Publish --> NormalQ("order_normal_queue")
        Publish --> DelayQ("order_delay_queue")
    end

    NormalQ --> Consumer1["Order Consumer<br/>INSERT t_order"]
    DelayQ --> Consumer2["Dead Letter Consumer<br/>check payment → cancel + compensate"]

    classDef client fill:#f9f9f9,stroke:#333,stroke-width:2px,color:#333333;
    classDef queue fill:#e1f5fe,stroke:#0288d1,stroke-width:1px,color:#333333;
    classDef consumer fill:#e8f5e9,stroke:#2e7d32,stroke-width:1px,color:#333333;

    class Client client;
    class NormalQ,DelayQ queue;
    class Consumer1,Consumer2 consumer;
```

---

## Core Tech

### 1. Anti-Oversell: Redis Lua Atomic Deduction

- A single Lua script atomically executes `GET` / `SISMEMBER` / `DECR` / `SADD` on Redis's single-threaded core
- Verified under load: 200 concurrent requests against 1 unit of stock — **exactly 1 success**
- The script is uploaded once at startup via `ScriptLoad`; requests reuse the 40-byte SHA1 with `EvalSha` to cut network payload

### 2. Anti-Duplicate: Per-User Distributed Lock (2s TTL + Watchdog renewal)

- UUID + SETNX for ownership-tagged acquisition
- A `Watchdog` goroutine renews the TTL every `TTL/3` via a Lua `check-and-PEXPIRE`
- `Unlock` uses a Lua `check-and-DEL` so a slow process can never erase another holder's lock

### 3. Reliable-Message Eventual Consistency: RabbitMQ Dual-Queue + Dead Letter Compensation

> Distinct from TCC / Saga patterns. This is the canonical "reliable message" approach: a message is durably persisted in RabbitMQ, and the dead-letter consumer guarantees the system will eventually reconcile DB and Redis state even if individual steps fail or restart.

- On successful reserve, the message is fan-published to both `order_normal_queue` (immediate `INSERT t_order`) and `order_delay_queue` (15-min TTL)
- When the delay queue's TTL fires, the message dead-letters to `order_dead_queue` → consumer queries payment status → if still unpaid, marks the order cancelled and triggers Redis compensation
- `compensate.lua` is idempotent via a `SISMEMBER` guard — safe under redelivery or partial failures

### 4. Retry Mechanism: Dedicated Delay Queues + Header Counter

- On transient DB failure, the consumer republishes the message into a retry queue (1-min TTL) that automatically loops back to the origin
- `Headers["x-retry-count"]` is incremented each round; after 3 attempts the message is logged for manual intervention and physically removed
- The normal-queue and dead-letter consumers have **separate** retry queues to prevent cross-contamination

---

## Performance

### Test Environment
WSL2 (Ubuntu) + Redis/MySQL/RabbitMQ Docker single-machine deployment. 
`ulimit -n 65535`, Redis AOF persistence enabled (`appendonly yes`), RabbitMQ delivery_mode=2 (persistent messages).
8 threads / 200 connections / 30-second load test 

### Group A: HTTP Layer Ceiling (no Lua / Redis hit)

| Stage | Optimization | QPS | Avg Latency |
|-------|--------------|-----|-------------|
| Baseline | `gin.Default()` + debug mode | 25,130 | 75 ms |
| Strip access log | `gin.SetMode(Release)` + `gin.New()` + Recovery | **429,586** | **844 µs** |

> In this group requests are rejected by upstream middleware via the fast path; the number reflects raw HTTP framework throughput.

### Group B: End-to-End Business Throughput (full Redis Lua path)

| Metric | Value |
|--------|-------|
| QPS | **50,365** |
| Avg Latency | 4.23 ms |
| Latency stddev | 5.45 ms |
| Bottleneck | go-redis `EvalSha` network RTT (flame-graph evidence) |

> This group exercises the full `Reserve → EvalSha → Lua` chain. Of the 4.23 ms average latency, roughly 4 ms is Redis network round-trip.

---

## Flame Graph Analysis

### Figure 1: Baseline

![baseline flame graph](flame_baseline.png)

Before optimization: `gin.LoggerWithConfig` → `fmt.Fprint` → `syscall.Write` dominates CPU. The hidden cost is one stderr `write(2)` per request.

---

### Figure 2: Optimized

![optimized flame graph](flame_optimized.png)

After optimization: the business chain `service.Reserve` → `go-redis.EvalSha` → `runtime.netpoll` is clearly visible. The bottleneck has shifted entirely to Redis network I/O with no remaining business-code hotspots.

---

### Figure 3: System Health

![goroutine profile](goroutine_profile.png)

The goroutine profile shows all 390 goroutines healthy under 200 concurrency — blocked only on HTTP read and Redis I/O. No mutex contention, no GC pauses, no I/O backlog.

---

## Deployment Architecture

End-to-end CI/CD on GitHub Actions. Every push to `main` triggers three sequential jobs:

```mermaid
graph LR
    Push[git push main] --> CI[GitHub Actions]
    CI --> Test["Job 1<br/>go test -race ./..."]
    Test --> Build["Job 2<br/>Docker multi-stage build<br/>push ghcr.io/.../aegis-gateway:SHA"]
    Build --> Deploy["Job 3<br/>SSH to VPS<br/>sed IMAGE_TAG → compose pull → up -d<br/>poll /healthz → rollback on failure"]
    Deploy --> VPS[(Production VPS<br/>aegis-gateway + MariaDB<br/>+ Redis + RabbitMQ)]

    classDef job fill:#e8f5e9,stroke:#2e7d32,color:#333;
    classDef target fill:#fff3e0,stroke:#e65100,color:#333;
    class Test,Build,Deploy job;
    class VPS target;
```

**Design choices:**

- **Image tag = commit SHA**: every deploy is uniquely identifiable; rollback is `IMAGE_TAG=<previous_SHA>` — no rebuild required.
- **Two compose files**: `docker-compose.yml` for local (`build: .`) vs `docker-compose.prod.yml` for VPS (`image: ghcr.io/...`). Prevents dev/prod config cross-contamination.
- **Dedicated CI SSH key**: a separate ed25519 keypair scoped only to deploy; personal SSH key never touches GitHub Secrets.
- **PAT for VPS image pull**: a token with `read:packages` only — leak blast radius is read-only, distinct from the build-time `GITHUB_TOKEN`.
- **`sed -i` over `>` for `.env.prod`**: deploys patch `IMAGE_TAG` in place, preserving production credentials in the same file.
- **`/healthz` deep health check + automatic rollback**: a root-level `GET /healthz` (outside `/api/v1`, bypassing AntiSpam) actively pings Redis, MySQL, and the RabbitMQ channel — returning `503` if any dependency is down, `200 {"status":"healthy"}` otherwise. It is used in two places:
  - **Compose healthcheck**: the `aegis-gateway` service probes `/healthz` via `wget` (Alpine has no `curl`) every 10s, so Docker reports container health accurately.
  - **Post-deploy gate**: after `up -d`, the deploy job backs up the previous `IMAGE_TAG` (`.env.prod.bak`) and polls `/healthz` up to 20 times (3s apart, ~60s max). On success it prunes dangling images and exits 0; if all 20 probes fail, it `sed`s `IMAGE_TAG` back to the previous SHA, redeploys, and exits 1 so CI turns red and a broken image never stays live.

---

## Data Schema

The MySQL side stores only the order record. Redis owns the hot path (stock counter + buyer set).

```sql
CREATE TABLE t_order (
    order_no    VARCHAR(64)  PRIMARY KEY COMMENT 'UUID v4 with ORD- prefix',
    user_id     VARCHAR(32)  NOT NULL,
    resource_id BIGINT       NOT NULL,
    status      TINYINT      NOT NULL DEFAULT 0
                COMMENT '0=pending  1=paid  2=cancelled',
    created_at  DATETIME     DEFAULT CURRENT_TIMESTAMP,
    updated_at  DATETIME     DEFAULT CURRENT_TIMESTAMP
                                       ON UPDATE CURRENT_TIMESTAMP,
    UNIQUE KEY  uk_user_resource (user_id, resource_id)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_unicode_ci;
```

**Design trade-offs:**

- **PK = VARCHAR(order_no)**: UUID is distributed-friendly (no coordinator), but random inserts cause B+Tree page splits. Acceptable here; switch to Snowflake if write throughput becomes the bottleneck.
- **`UNIQUE KEY` on `(user_id, resource_id)`** — *defense in depth*: Redis `SISMEMBER` is the first line of defense (O(1), fast path). But Redis can lose in-memory state on restart or failover, so MySQL's UNIQUE constraint serves as the **last-resort backstop** — even if Redis is compromised, duplicate orders are still rejected at the DB layer.
- **`DATETIME` instead of `TIMESTAMP`**: TIMESTAMP overflows at 2038-01-19 (32-bit). DATETIME covers up to year 9999 at a 3-byte overhead — trivial compared to the data and indexes.

Full schema in [`scripts/init.sql`](scripts/init.sql).

---

## Project Structure

```text
aegis-gateway/
├── .github/
│   └── workflows/
│       └── cicd.yml                     # CI/CD pipeline: test → build image → push ghcr.io → SSH deploy
├── cmd/
│   └── api/
│       └── main.go                      # Entry point: initializes components and starts the HTTP server
├── internal/
│   ├── api/
│   │   ├── handler/
│   │   │   └── reserve.go               # Request parsing, validation, dispatch to service
│   │   └── middleware/
│   │       ├── ratelimit.go             # IP token-bucket rate limit (golang.org/x/time/rate)
│   │       └── anti_spam.go             # Per-user distributed lock anti-duplicate
│   ├── bootstrap/
│   │   ├── db.go                        # MySQL connection pool init
│   │   ├── redis.go                     # Redis init + Lua script preload (ScriptLoad)
│   │   ├── rabbitmq.go                  # Declares exchange, queues, bindings
│   │   └── router.go                    # Gin router setup; reads APP_MODE to toggle rate limits
│   ├── consumer/
│   │   ├── order_consumer.go            # Normal order consumer: listens on order_normal_queue, calls InsertOrder
│   │   ├── dead_letter_consumer.go      # Dead letter consumer: unpaid timeout → cancel order + compensate Redis
│   │   └── helper.go                    # Shared utilities: getRetryCount (safe AMQP header type assertion)
│   ├── global/
│   │   └── global.go                    # Global singletons (DB, Redis, ReserveSHA, CompensateSHA, MQChannel)
│   ├── repository/
│   │   ├── mysql_repo.go                # InsertOrder / GetOrderByUserAndResource / UpdateOrderStatus
│   │   └── redis_repo.go                # Redis ops wrappers
│   └── service/
│       ├── reserve_service.go           # Core business: EvalSha Lua + dual MQ publish
│       └── reserve_service_test.go      # Concurrency test: 200 goroutines fight for 1 stock unit
├── pkg/
│   └── distributed_lock/
│       ├── lock.go                      # Distributed lock: SETNX+TTL, Lua atomic unlock, watchdog renewal
│       └── lock_test.go                 # Lock unit tests
├── scripts/
│   ├── lua/
│   │   ├── reserve.lua                  # Atomic stock deduct: stock check → SISMEMBER → DECR + SADD
│   │   └── compensate.lua               # Idempotent compensation: SISMEMBER guard → INCR + SREM
│   ├── wrk.sh                           # Generates wrk load-test Lua script at /tmp/reserve.lua
│   └── test_day3.sh                     # Day 3 smoke test script
├── go.mod
├── go.sum
├── docker-compose.yml                   # Local dev stack: MySQL + Redis + RabbitMQ + builds aegis-gateway from source
├── docker-compose.prod.yml              # Production stack on VPS: pulls aegis-gateway image from ghcr.io
├── Dockerfile                           # Multi-stage build: golang:1.26-alpine → alpine:3.19
├── .env.example                         # Credential template (copy to .env and fill in)
└── LICENSE
```

---

## Quick Start

### Option A — Docker (recommended)

```bash
# 1. Configure credentials
cp .env.example .env        # fill in passwords

# 2. Start everything
docker compose up --build -d

# 3. Create schema
docker exec -it appoint_mysql mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" appoint_db < scripts/init.sql
```

### Option B — Local (for development)

```bash
# 1. Configure credentials
cp .env.example .env        # set REDIS_ADDR=127.0.0.1:6379, MYSQL_ADDR=127.0.0.1:3309, etc.

# 2. Start dependencies only
docker compose up -d mysql redis rabbitmq

# 3. Create schema
docker exec -it appoint_mysql mysql -uroot -p"${MYSQL_ROOT_PASSWORD}" appoint_db < scripts/init.sql

# 4. Run the service
go run cmd/api/main.go
```

### Load Test

```bash
APP_MODE=loadtest go run cmd/api/main.go    # disables anti-spam middleware
bash scripts/wrk.sh
wrk -t8 -c200 -d30s -s /tmp/reserve.lua http://localhost:8080/api/v1/reserve
```

---

## Roadmap

- [x] Graceful shutdown (SIGTERM waits for in-flight consumer work)
- [x] End-to-end CI/CD on GitHub Actions: tests → build & push image to ghcr.io → SSH deploy to VPS
- [x] Post-deploy health check + automatic rollback on failure (`/healthz` deep probe + 20x poll → rollback)
- [ ] Redis pipeline batching (expected +50% QPS)
- [ ] Local in-memory pre-deduction + async Redis sync (expected 5-10x)
- [ ] Dead letter table persistence (audit trail for operations)

---

## Tech Stack

Go 1.26 / Gin / go-redis v9 / amqp091-go / Lua / Docker Compose
