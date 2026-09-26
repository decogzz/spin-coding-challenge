# spin-coding-challenge

A REST API that executes financial transactions (CREDIT and DEBIT) against an
external provider, persists the outcome, and exposes the stored transactions for
querying. Written in Go with PostgreSQL, for the Spin backend challenge.

## Problem

- `POST /transactions` validates the request, applies the business rules, calls
  the provider, persists the result (including the balance the provider returns),
  and returns the transaction.
- `GET /transactions` lists stored transactions, filtered by `accountId`,
  `status` and `type`, paginated with `page` and `limit`.
- Business rules, enforced **before** the provider is called: the amount must be
  greater than 1.00; a DEBIT must not exceed 10,000.00; the currency must be MXN.
- The provider owns balances and account locks; this service only executes,
  records and exposes transactions. It is expected to run at millions of
  transactions per day.

## Quick start

Requirements: Docker with Compose.

```sh
docker compose up --build
```

This starts PostgreSQL (schema applied automatically), the provider mock on
`:8081`, and the API on `:8080`.

```sh
curl -i -X POST localhost:8080/transactions -H 'Content-Type: application/json' -d '{
  "accountId": "acc-123456",
  "type": "CREDIT",
  "amount": 1500.00,
  "currency": "MXN",
  "description": "Transferencia recibida"
}'
```

```http
HTTP/1.1 201 Created
Location: /transactions/01a0df06-5d40-7dbc-84e4-41d1089296fb

{"id":"01a0df06-5d40-7dbc-84e4-41d1089296fb","accountId":"acc-123456","type":"CREDIT","amount":1500.00,
 "currency":"MXN","description":"Transferencia recibida","status":"EXECUTED","providerTransactionId":"txn-1",
 "balanceAfter":11500.00,"createdAt":"2026-09-26T18:42:11.3929Z","updatedAt":"2026-09-26T18:42:11.397409Z"}
```

```sh
curl 'localhost:8080/transactions?accountId=acc-123456&status=EXECUTED&page=1&limit=20'
curl localhost:8080/transactions/01a0df06-5d40-7dbc-84e4-41d1089296fb
```

Stop and remove everything, including the database, with `docker compose down -v`.

### Trying the failure scenarios

The provider mock reserves some account IDs so every path can be reproduced by hand.
Any other account starts with a balance of 10,000.00.

| accountId          | Provider behaviour                                | API result                   |
| ------------------ | ------------------------------------------------- | ---------------------------- |
| `acc-insufficient` | Balance starts at 0, so DEBITs fail               | `422`, `REJECTED`, `INSUFFICIENT_FUNDS` |
| `acc-blocked`      | Account is locked                                 | `422`, `REJECTED`, `ACCOUNT_BLOCKED` |
| `acc-flaky`        | `503` on the first attempt of each request        | `201`, `EXECUTED` after one retry |
| `acc-error`        | Always `500`                                      | `502`, `FAILED` after 3 attempts |
| `acc-slow`         | Answers after 5s, beyond the 2s attempt timeout   | `502`, `FAILED` after about 6s |

Five consecutive failed executions open the circuit breaker. For the next 30
seconds every request fails fast with `502` without calling the provider, and then
a single probe decides whether to close it again. Watch it with
`docker compose logs -f api`.

## API

The full contract is in [`api/openapi.yaml`](api/openapi.yaml).

| Endpoint                   | Description                                    |
| -------------------------- | ---------------------------------------------- |
| `POST /transactions`       | Execute a transaction                          |
| `GET /transactions`        | List, newest first; filters `accountId`, `status`, `type`; `page` (default 1), `limit` (default 20, max 100) |
| `GET /transactions/{id}`   | Get one transaction                            |
| `GET /healthz`             | Liveness: the process is up                    |
| `GET /readyz`              | Readiness: the database answers a ping         |

`POST /transactions` returns the transaction in every outcome, and the HTTP status
tells the client which one happened:

| Status | Meaning |
| ------ | ------- |
| `201`  | `EXECUTED`: the provider approved it; `balanceAfter` and `providerTransactionId` are set. |
| `422`  | `REJECTED`: the provider refused it for a business reason; `failureCode` and `failureMessage` explain why. |
| `502`  | `FAILED`: the provider could not confirm the outcome (unreachable, 5xx after retries, timeout, or open circuit). |
| `400`  | The request broke a validation or business rule. Nothing was executed or stored. |

Errors share one shape, which mirrors the provider's error body. Every broken
rule is reported at once:

```json
{
  "code": "VALIDATION_FAILED",
  "message": "the request is invalid; see details",
  "details": [
    {"field": "amount", "code": "DEBIT_LIMIT_EXCEEDED", "message": "DEBIT amount must not exceed 10000.00 per transaction"},
    {"field": "currency", "code": "UNSUPPORTED_CURRENCY", "message": "currency must be MXN"}
  ]
}
```

## Approach

```mermaid
flowchart LR
  Client --> HttpApi["httpapi: handlers, DTOs, error mapping"]
  HttpApi --> Service["transaction.Service"]
  Service --> Rules["business rules"]
  Service --> RepoPort["Repository interface"]
  Service --> ProviderPort["Provider interface"]
  RepoPort -.-> Postgres["postgres: pgx"]
  RepoPort -.-> Memory["memory: tests"]
  ProviderPort -.-> ProviderClient["provider: HTTP client, retries, circuit breaker"]
  ProviderClient --> ProviderMock["providermock"]
```

The domain package, `internal/transaction`, holds the model, the rules and the
service, and it defines the two interfaces it depends on: `Repository` and
`Provider`. The adapters (PostgreSQL, the HTTP provider client, the HTTP API)
depend on the domain, never the other way around. That keeps the business logic
testable without a database or network, and makes either side replaceable.

### Execution flow

```mermaid
sequenceDiagram
  participant C as Client
  participant S as Service
  participant DB as PostgreSQL
  participant P as Provider
  C->>S: POST /transactions
  S->>S: validate and apply rules
  S->>DB: insert PENDING
  S->>P: execute (Idempotency-Key = transaction id)
  P-->>S: APPROVED, REJECTED or error
  S->>DB: update status, providerTransactionId, balanceAfter
  S-->>C: 201, 422 or 502
```

- **The transaction is stored as `PENDING` before the provider is called.** If
  the process dies mid-call, a record still exists, so the transaction can be
  reconciled rather than silently lost.
- **A client disconnect does not abort an execution in progress.** Once the
  `PENDING` row exists, the provider call and the final update run on a context
  detached from the request's cancellation (`context.WithoutCancel`). The
  provider client's own timeouts bound how long this takes.

## Design decisions

**PostgreSQL for persistence.** Transactions are financial records: they need
ACID writes, constraints (`CHECK` on type, status and amount), and indexed
filtering. PostgreSQL does all of that, scales well for this access pattern, and
is part of Spin's stack. An in-memory repository implements the same interface
for unit tests, and one shared contract test suite (`internal/repotest`) runs
against both, so the fake cannot drift from the real thing.

**Money is an integer number of centavos.** `transaction.Money` is an `int64`,
stored as `BIGINT`. Amounts arrive as JSON numbers and are parsed from their
exact decimal text, never through `float64`. More than two decimal places or an
exponent (`1e3`) is rejected rather than rounded. Responses write amounts as
numbers with two decimals, such as `1500.00`.

**Business rules are a list of small functions.** Each `Rule` checks one thing
and returns a violation or nothing. The service runs all of them and reports every
violation together. Adding a rule means writing one function and adding it to
`DefaultRules`.

**Provider resilience.** The provider client in `internal/provider`:
- bounds each HTTP attempt with a timeout (2s by default);
- retries up to 3 attempts with exponential backoff and jitter, but **only** on
  network errors, `5xx` and `429`; a `4xx` is a business answer and is never
  retried;
- wraps each whole execution in a circuit breaker (`sony/gobreaker`). Business
  rejections count as successes, since the provider is healthy and answering;
- reuses connections with a tuned HTTP transport. Go's default of 2 idle
  connections per host causes constant reconnects under load.

**Retries are safe because of an idempotency key.** Every attempt for a
transaction sends `Idempotency-Key: <transaction id>`. The provider contract in
the challenge doesn't define this header, so this is an **assumption**: a real
provider would need to support it (or something equivalent) for retries to be
safe. Without it, retrying a timed-out DEBIT could charge twice. The mock honours
the header.

**A timeout means "unknown", not "failed".** When the provider doesn't answer,
the money may or may not have moved. The transaction is stored as `FAILED` with
`failureCode: PROVIDER_UNAVAILABLE`, meaning "not confirmed". In production, a
reconciliation job would query the provider for `PENDING` and `FAILED`
transactions and settle their real status.

**Pagination without `COUNT(*)`.** `GET /transactions` fetches `limit + 1` rows
and returns `hasMore` instead of a total. Counting millions of rows on every page
request gets expensive. Each filter has a matching index that includes the
ordering, `(filter, created_at DESC, id DESC)`, so a page is read straight off
the index without sorting.

**IDs are UUIDv7.** They are time-ordered, so inserts land at the end of the
primary-key index instead of at random pages, which matters at high write volume.

**Operational basics.** JSON structured logs (`log/slog`) that carry a
`request_id` (taken from `X-Request-ID` or generated) through to the service's
logs; panic recovery; a request body limit; server read and write timeouts; and
graceful shutdown on `SIGTERM` that drains in-flight requests. The write timeout
is derived from the provider settings, so it always outlasts the slowest possible
provider call.

## Running locally without Docker for the API

Requirements: Go 1.27 or newer (`winget install GoLang.Go` on Windows,
`brew install go` on macOS) and a PostgreSQL instance.

```sh
docker compose up -d postgres providermock
export DATABASE_URL='postgres://spin:spin@localhost:5432/transactions?sslmode=disable'
export PROVIDER_BASE_URL='http://localhost:8081'
go run ./cmd/api
```

### Configuration

| Variable                        | Default   | Description |
| ------------------------------- | --------- | ----------- |
| `DATABASE_URL`                  | required  | PostgreSQL URL. Pool settings go here too, e.g. `pool_max_conns=20`. |
| `PROVIDER_BASE_URL`             | required  | Base URL of the provider. |
| `HTTP_ADDR`                     | `:8080`   | Listen address. |
| `LOG_LEVEL`                     | `info`    | `debug`, `info`, `warn` or `error`. |
| `SHUTDOWN_TIMEOUT`              | `15s`     | Time allowed to drain requests on shutdown. |
| `PROVIDER_ATTEMPT_TIMEOUT`      | `2s`      | Timeout for a single provider attempt. |
| `PROVIDER_MAX_ATTEMPTS`         | `3`       | Attempts per execution, including the first. |
| `PROVIDER_BASE_BACKOFF`         | `100ms`   | Initial retry delay; doubles each retry. |
| `PROVIDER_MAX_BACKOFF`          | `1s`      | Cap on the retry delay. |
| `PROVIDER_BREAKER_FAILURES`     | `5`       | Consecutive failed executions that open the circuit. |
| `PROVIDER_BREAKER_OPEN_TIMEOUT` | `30s`     | How long the circuit stays open before a probe. |

The mock reads `PROVIDER_MOCK_ADDR` (`:8081`), `PROVIDER_MOCK_INITIAL_BALANCE`
(`10000.00`) and `PROVIDER_MOCK_SLOW_DELAY` (`5s`).

## Testing

```sh
go test ./...                     # unit tests
go test -race ./...               # with the race detector (needs cgo)
make test-integration             # PostgreSQL tests; needs `docker compose up -d postgres`
```

The integration tests sit behind the `integration` build tag and are skipped
unless `TEST_DATABASE_URL` is set. They apply the migration themselves.

What is covered:
- **Rules:** the boundaries 1.00 (rejected), 1.01, 10,000.00 (allowed for DEBIT)
  and 10,000.01, other currencies, CREDIT without an upper limit, and several
  violations reported together.
- **Service:** executed, rejected and failed outcomes; a broken rule never reaches
  the provider or the database; a database failure never reaches the provider; a
  client disconnect still records the outcome.
- **Provider client**, against `httptest` servers: response mapping, no retry on
  `4xx`, retry on `5xx` and `429`, the same idempotency key on every attempt, the
  per-attempt timeout, a malformed success not being retried, and the circuit
  opening while rejections leave it closed.
- **HTTP API:** status codes for every outcome, validation and malformed bodies,
  filters and pagination, not-found, health checks, request ID propagation, and
  panics turned into a generic `500`.
- **Repository contract:** one suite run against both the memory and PostgreSQL
  repositories.
- **Mock plus client end to end:** balances, idempotent replays, and every
  scenario account through the real provider client.

Coverage of the domain, API, provider and repository packages is 87 to 100%. The
reported total is lower because `cmd/` wiring isn't unit-tested and the PostgreSQL
adapter is covered by the integration job instead.

CI (`.gitlab-ci.yml`) runs gofmt, `go vet`, golangci-lint, unit tests with `-race`
and coverage, and the integration tests against a `postgres:16` service.

## Project layout

```
cmd/api/                 API entry point: config, wiring, graceful shutdown
cmd/providermock/        provider mock entry point
internal/transaction/    domain: model, Money, rules, service, Repository and Provider interfaces
internal/provider/       HTTP client for the provider: timeouts, retries, circuit breaker
internal/postgres/       PostgreSQL repository (pgx)
internal/memory/         in-memory repository for tests
internal/repotest/       contract tests shared by both repositories
internal/httpapi/        handlers, DTOs, error mapping, middleware
internal/providermock/   mock provider logic
internal/requestid/      request ID propagation into logs
internal/config/         environment configuration
migrations/              SQL schema
api/openapi.yaml         OpenAPI 3.1 contract
```

## Trade-offs and next steps

Kept deliberately simple:
- **Offset pagination**, because the challenge asks for `page` and `limit`. Deep
  offsets get slower as the table grows; keyset (cursor) pagination on
  `(created_at, id)` would keep every page equally fast, and the indexes already
  support it.
- **Schema applied through `docker-entrypoint-initdb.d`**, which only runs on a
  fresh database. A real deployment would use a migration tool (golang-migrate,
  goose, Atlas) as a separate step.
- **The mock keeps its state in memory** and forgets balances on restart.
- **One circuit breaker for the whole provider.** If the provider has per-account
  or per-region failure modes, breakers could be partitioned.
- **No client-side idempotency on `POST /transactions`.** A client that retries
  after a network error creates a second transaction. The fix is an
  `Idempotency-Key` header stored in a unique column, which returns the original
  transaction on a repeat.

At higher scale:
- The service is stateless, so it scales horizontally behind a load balancer.
  Database connections are the constraint: size `pool_max_conns` per instance, and
  put PgBouncer in front if instances multiply.
- Partition `transactions` by `created_at` (monthly) to keep indexes small and make
  archiving cheap, with read replicas serving `GET` traffic.
- Publish each outcome as an event (Kafka) through a transactional outbox, so
  downstream systems react without polling and without dual-write inconsistencies.
- Add a reconciliation worker for `PENDING` and `FAILED` transactions, plus metrics
  and tracing (OpenTelemetry) for provider latency, retry rate and breaker state.
- If provider latency becomes the bottleneck, accept requests with `202` and
  execute them asynchronously from a queue.

## Use of AI

I used Cursor's AI agent (Claude) throughout this challenge:
- **Planning:** I gave it the challenge PDF and discussed the design with it. I
  chose the stack (Go and PostgreSQL) and approved the plan (architecture, status
  codes, resilience strategy, mock design) before any code was written.
- **Implementation:** the agent wrote the code, tests, Docker and CI setup, and
  this README, one layer at a time, committing after each step.
- **Verification:** each step was checked by running the tests, golangci-lint,
  the race detector and the integration tests in the CI images, and by exercising
  every provider scenario with curl against the Docker Compose stack.
