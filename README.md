# spin-coding-challenge

A REST API that executes financial transactions (CREDIT and DEBIT) against an
external provider, persists the outcome, and exposes the stored transactions for
querying. Written in Python (FastAPI) with PostgreSQL, for the Spin backend challenge.

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

## Stack

| Layer | Choice | Why |
| ----- | ------ | --- |
| Language | Python 3.14 | Mature async ecosystem; 3.14 adds `uuid.uuid7()` to the standard library, used for the ids. |
| HTTP | FastAPI + Uvicorn | Async, typed request handling and an OpenAPI document generated from the code. |
| Database | PostgreSQL 16 | See [Design decisions](#design-decisions). |
| Data access | SQLAlchemy 2 Core + asyncpg, Alembic | Explicit SQL without an ORM's hidden queries; versioned migrations. |
| Provider client | httpx | Async client with connection pooling and a pluggable transport for tests. |
| Tooling | uv, ruff, mypy (strict), pytest | Locked, reproducible installs; one fast linter and formatter; types checked in CI. |

The tools used to build it:

- **Cursor** as the editor. It is stronger than plain Visual Studio Code for this
  kind of work: its agent works directly in the codebase and terminal, and it
  integrates skills, MCP servers (GitLab, Terraform, documentation lookups) and
  subagents.
- **GitLab** for the repository and CI, because security scanning is built in:
  SAST and Secret Detection run in the pipeline with no extra services, and lint
  findings show up in merge requests as a Code Quality report.
- **Claude Opus 5.5** as the model behind the agent. It is one of the strongest
  models available while staying cost-effective for a task this size: design,
  concurrency and failure handling. For simpler, mechanical tasks I would have
  used Sonnet or Grok instead.

The first version was written in Go and is kept on the
[`go-implementation`](https://gitlab.com/decogzz/spin-coding-challenge/-/tree/go-implementation)
branch. The Python version keeps the same API contract and design.

## Quick start

Requirements: Docker with Compose.

```sh
docker compose up --build
```

This starts PostgreSQL, runs the Alembic migrations once (`migrate` service), and
starts the provider mock on `:8081` and the API on `:8080`. Interactive API docs
are at <http://localhost:8080/docs>.

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
Location: /transactions/01a0e8a0-9040-70af-94ca-80907aa2f268

{"id":"01a0e8a0-9040-70af-94ca-80907aa2f268","accountId":"acc-123456","type":"CREDIT","amount":1500.00,
 "currency":"MXN","description":"Transferencia recibida","status":"EXECUTED","providerTransactionId":"txn-5",
 "balanceAfter":11500.00,"failureCode":null,"failureMessage":null,
 "createdAt":"2026-09-28T15:27:11.936042Z","updatedAt":"2026-09-28T15:27:11.940856Z"}
```

```sh
curl 'localhost:8080/transactions?accountId=acc-123456&status=EXECUTED&page=1&limit=20'
curl localhost:8080/transactions/01a0e8a0-9040-70af-94ca-80907aa2f268
```

`uv run python scripts/smoke.py` exercises every scenario below against the
running stack, including a full circuit breaker cycle (it takes about 40 seconds).
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

The OpenAPI document is generated from the code, so it cannot drift from the
implementation: Swagger UI at `/docs`, the raw document at `/openapi.json`.

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
| `413`  | The body is larger than 64 KiB. |

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
  Client --> Api["api: routes, schemas, error mapping, middleware"]
  Api --> Service["domain.TransactionService"]
  Service --> Rules["domain.rules"]
  Service --> RepoPort["TransactionRepository protocol"]
  Service --> ProviderPort["TransactionProvider protocol"]
  RepoPort -.-> Postgres["adapters.postgres: SQLAlchemy Core + asyncpg"]
  RepoPort -.-> Memory["adapters.memory: tests"]
  ProviderPort -.-> ProviderClient["adapters.provider_client: httpx, retries, circuit breaker"]
  ProviderClient --> ProviderMock["providermock"]
```

The `domain` package holds the model, the rules and the service, and it defines
the two interfaces it depends on as `Protocol`s in `domain/ports.py`:
`TransactionRepository` and `TransactionProvider`. The adapters (PostgreSQL, the
HTTP provider client, the HTTP API) depend on the domain, never the other way
around; `main.py` is the only module that wires concrete classes together. That
keeps the business logic testable without a database or network, and makes either
side replaceable.

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
  `PENDING` row exists, the provider call and the final update run in their own
  task behind `asyncio.shield`, so cancelling the request doesn't cancel them. On
  shutdown the service waits for those tasks (up to `SHUTDOWN_TIMEOUT`) before
  closing the database pool.

## Design decisions

**PostgreSQL for persistence.** Transactions are financial records: they need
ACID writes, constraints (`CHECK` on type, status and amount), exact decimal
types and indexed filtering. PostgreSQL does all of that, scales well for this
access pattern, and is part of Spin's stack. An in-memory repository implements
the same protocol for unit tests, and one shared contract suite
(`tests/repository_contract.py`) runs against both, so the fake cannot drift from
the real thing. An integration test also checks that the Alembic migration matches
the table definition in code.

**Money is `Decimal`, never `float`.** Amounts are stored as `NUMERIC(17, 2)`.
FastAPI and pydantic parse JSON numbers through `float`, which silently turns
`999999999999999.99` into `1e15`, so this API reads request bodies itself with
`json.loads(parse_float=Decimal)` and writes responses with `simplejson`, which
emits `Decimal` as an exact JSON number (`1500.00`). The provider client and the
mock do the same. Amounts with more than two decimal places are rejected rather
than rounded; `1.500` and `1e3` are accepted and normalized to `1.50` and `1000.00`.

**Business rules are a list of small functions.** Each rule checks one thing and
returns a violation or `None`. The service runs all of them and reports every
violation together. Adding a rule means writing one function and adding it to
`DEFAULT_RULES`.

**Provider resilience.** The provider client in `adapters/provider_client.py`:
- bounds each HTTP attempt with a timeout (2s by default);
- retries up to 3 attempts with exponential backoff and jitter, but **only** on
  network errors, timeouts, `5xx` and `429`; a `4xx` is a business answer and is
  never retried, and neither is a malformed `200`, because the money may already
  have moved;
- wraps each whole execution in a circuit breaker (`adapters/circuit_breaker.py`,
  about 100 lines, so its behaviour is fully visible and tested). Business
  rejections count as successes, since the provider is healthy and answering;
- keeps a pool of keep-alive connections, so requests don't pay for a new
  connection each time.

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
request gets expensive. The filters have matching indexes that include the
ordering, `(filter, created_at DESC, id DESC)`, so a page is read straight off
the index without sorting.

**IDs are UUIDv7.** They are time-ordered, so inserts land at the end of the
primary-key index instead of at random pages, which matters at high write volume.

**Operational basics.** JSON structured logs that carry a `request_id` (taken
from `X-Request-ID` or generated) into every log line of the request, including
the provider client's; an access log line per request; unhandled errors turned
into a generic `500` without leaking details; a 64 KiB request body limit;
`/readyz` pinging the database; migrations as a separate one-shot step; a
non-root container; and graceful shutdown on `SIGTERM`. The middleware is written
against raw ASGI rather than Starlette's `BaseHTTPMiddleware`, which adds a task
per request and interferes with cancellation.

## Running locally without Docker for the API

Requirements: [uv](https://docs.astral.sh/uv/) (it installs Python 3.14 if needed).

```sh
docker compose up -d postgres providermock
uv sync
export DATABASE_URL='postgresql+asyncpg://spin:spin@localhost:5432/transactions'
export PROVIDER_BASE_URL='http://localhost:8081'
uv run alembic upgrade head
uv run spin-api
```

### Configuration

Durations are in seconds.

| Variable                        | Default   | Description |
| ------------------------------- | --------- | ----------- |
| `DATABASE_URL`                  | required  | PostgreSQL URL; `postgres://` and `postgresql://` are accepted too. |
| `PROVIDER_BASE_URL`             | required  | Base URL of the provider. |
| `HOST` / `PORT`                 | `0.0.0.0` / `8080` | Listen address. |
| `LOG_LEVEL`                     | `INFO`    | `DEBUG`, `INFO`, `WARNING` or `ERROR`. |
| `SHUTDOWN_TIMEOUT`              | `15`      | Time allowed to finish in-flight executions on shutdown. |
| `DB_POOL_SIZE` / `DB_MAX_OVERFLOW` | `10` / `10` | Database connections kept open / allowed on top under load. |
| `DB_POOL_TIMEOUT`               | `5`       | Wait for a free connection before failing. |
| `PROVIDER_ATTEMPT_TIMEOUT`      | `2`       | Timeout for a single provider attempt. |
| `PROVIDER_MAX_ATTEMPTS`         | `3`       | Attempts per execution, including the first. |
| `PROVIDER_BASE_BACKOFF`         | `0.1`     | Initial retry delay; doubles each retry. |
| `PROVIDER_MAX_BACKOFF`          | `1`       | Cap on the retry delay. |
| `PROVIDER_BREAKER_FAILURES`     | `5`       | Consecutive failed executions that open the circuit. |
| `PROVIDER_BREAKER_OPEN_TIMEOUT` | `30`      | How long the circuit stays open before a probe. |

Invalid or missing settings are all reported at startup. The mock reads
`PROVIDER_MOCK_PORT` (`8081`), `PROVIDER_MOCK_INITIAL_BALANCE` (`10000.00`) and
`PROVIDER_MOCK_SLOW_DELAY` (`5`).

## Testing

```sh
uv run pytest                          # unit tests
uv run pytest --cov                    # with coverage
docker compose up -d postgres
TEST_DATABASE_URL='postgresql://spin:spin@localhost:5432/transactions' uv run pytest -m integration
uv run ruff check . && uv run ruff format --check . && uv run mypy
```

The integration tests are marked `integration`, are skipped unless
`TEST_DATABASE_URL` is set, and run the Alembic migrations (down and up) themselves.
They reset the schema, so point them at a database whose data you don't need.

What is covered:
- **Rules:** the boundaries 1.00 (rejected), 1.01, 10,000.00 (allowed for DEBIT)
  and 10,000.01, other currencies, CREDIT without an upper limit, precision and
  out-of-range amounts, and several violations reported together.
- **Service:** executed, rejected and failed outcomes; a broken rule never reaches
  the provider or the database; a database failure never reaches the provider; a
  client disconnect still records the outcome.
- **Provider client**, through `httpx.MockTransport`: response mapping with exact
  decimals, no retry on `4xx`, retry on `5xx`, `429` and network errors, the same
  idempotency key on every attempt, the per-attempt timeout, a malformed success
  not being retried, and the circuit opening while rejections leave it closed.
- **Circuit breaker:** opening, fast failure, the half-open single probe, and a
  cancelled probe not wedging it.
- **HTTP API:** status codes for every outcome, exact amounts end to end,
  validation and malformed bodies, filters and pagination, not-found, health
  checks, request ID propagation, and unexpected errors turned into a generic `500`.
- **Repository contract:** one suite run against both the memory and PostgreSQL
  repositories.
- **Mock plus client end to end:** balances, idempotent replays, and every
  scenario account through the real provider client.

Unit test coverage is 89%. Most of what is left is the PostgreSQL adapter, which
the integration job covers instead.

CI (`.gitlab-ci.yml`) runs ruff and mypy (with a Code Quality report), unit tests
with coverage and JUnit reports, the integration tests against a `postgres:16`
service, a Docker image build, GitLab SAST and Secret Detection, and `pip-audit`
against the locked dependencies.

## Project layout

```
src/spin_transactions/
  domain/                model, money, rules, errors, service, repository and provider protocols
  adapters/              PostgreSQL and in-memory repositories, provider HTTP client, circuit breaker
  api/                   FastAPI app, routes, schemas, error mapping, ASGI middleware
  providermock/          mock provider and its entry point
  config.py              settings from environment variables
  logs.py                JSON logging with the request id
  jsonutil.py            exact-decimal JSON encoding and decoding
  main.py                composition root and entry point
migrations/              Alembic migrations
tests/unit/              unit tests (no network or database)
tests/integration/       PostgreSQL tests
scripts/smoke.py         end-to-end check against the running stack
```

## Trade-offs and next steps

Kept deliberately simple:
- **Offset pagination**, because the challenge asks for `page` and `limit`. Deep
  offsets get slower as the table grows; keyset (cursor) pagination on
  `(created_at, id)` would keep every page equally fast, and the indexes already
  support it.
- **The mock keeps its state in memory** and forgets balances on restart.
- **One circuit breaker per API process, for the whole provider.** Each instance
  learns about provider failures on its own; a shared breaker (for example in
  Redis) or per-account/per-region breakers would be the next step if needed.
- **No client-side idempotency on `POST /transactions`.** A client that retries
  after a network error creates a second transaction. The fix is an
  `Idempotency-Key` header stored in a unique column, which returns the original
  transaction on a repeat.
- **A single Uvicorn worker per container.** Scaling is done by running more
  containers, which keeps connection pools and the breaker easy to reason about.

At higher scale:
- The service is stateless, so it scales horizontally behind a load balancer.
  Database connections are the constraint: size `DB_POOL_SIZE` per instance, and
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

I used Cursor's agent with Claude Opus 5.5 throughout this challenge:
- **Planning:** I gave it the challenge PDF and discussed the design with it. I
  chose the stack and the tools, and approved the plan (architecture, status
  codes, resilience strategy, mock design) before any code was written. The
  solution was first built in Go; I then asked for the move to Python, keeping
  the same design.
- **Implementation:** the agent wrote the code, tests, Docker and CI setup, and
  this README, one layer at a time, committing after each step.
- **Verification:** each step was checked by running ruff, mypy, the unit tests
  and the integration tests against a real PostgreSQL, and by running the smoke
  script against the Docker Compose stack to exercise every provider scenario and
  the circuit breaker cycle. Along the way this caught real problems, such as
  FastAPI parsing amounts through `float` and `Decimal.quantize` failing on very
  large values, both fixed and covered by tests.
