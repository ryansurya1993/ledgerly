# notification-service

Consumes transaction events from RabbitMQ and streams them live over
Server-Sent Events, for the frontend's live activity feed. Publishes
nothing and owns no data of its own — Postgres (via ledger-service)
remains the single source of truth for everything this service
displays. See the root `README.md` and `CLAUDE.md` for the overall
Ledgerly architecture.

## How this fits together

```
ledger-service --(RabbitMQ: ledger.events exchange)--> notification-service --(SSE: GET /events)--> browser
```

- `internal/events` — connects to RabbitMQ, consumes `transaction.posted`
  events, and reconnects with backoff if the connection drops. See
  `Consumer`'s doc comment for the fan-out topology this depends on and
  why it never gives up.
- `internal/stream` — `Hub`, an in-memory fan-out of event payloads to
  every currently-connected SSE client.
- `internal/handler` — `GET /health` (RabbitMQ connectivity) and
  `GET /events` (the SSE stream itself).

## Why SSE, not a WebSocket

This is a one-way, server-to-client-only feed: the client only ever
listens, never sends anything back over this connection. SSE is built
for exactly that shape, and it's the smaller tool for the job —
plain HTTP, served with nothing beyond `net/http` and `http.Flusher`
(unlike a WebSocket, which needs a third-party library since
`net/http` has no WebSocket implementation built in), and browsers'
native `EventSource` API reconnects automatically on a dropped
connection with no client-side code required. A WebSocket would be the
right call the moment this needs to carry client-to-server messages
too — it doesn't today.

## Why every consumer gets its own queue (fan-out, not a work queue)

This is the one genuinely easy-to-get-wrong decision in this service.
RabbitMQ's default behavior for multiple consumers on the *same* named
queue is competing-consumer load balancing: each message goes to
exactly one consumer, round-robin. That's correct for a job queue, but
it would be a real bug here — a live activity feed is supposed to
broadcast every event to every connected browser client, on every
`notification-service` replica, not distribute events across replicas
so each client only sees a fraction of them.

Instead, every `Consumer` — meaning every replica, and every reconnect
of a single replica — declares its own anonymous, exclusive,
auto-delete queue and binds it to ledger-service's `ledger.events`
topic exchange itself. That's RabbitMQ's standard pub/sub pattern (a
topic/fanout exchange plus one private queue per subscriber), and it's
what makes this a true broadcast that scales horizontally instead of
silently dropping most events from each client's point of view. The
queue needs no persistence: it dies with the connection, and there's
nothing worth persisting anyway — see "Delivery guarantee" below.

## Delivery guarantee: best-effort, same as the publish side

ledger-service publishes best-effort, after its own database
transaction has already committed (see its README's "Events" section
for the full reasoning) — Postgres is the durable source of truth, not
this pipeline. `notification-service` doesn't add a guarantee on top of
that: messages are consumed with `autoAck` (no manual acknowledgment,
no redelivery), and a message this service never received simply never
appears in the feed. A dropped event here means exactly one missed
update to a nice-to-have live view — never anything about the
underlying ledger, which is unaffected either way.

## Degrading sensibly when RabbitMQ is unreachable

`Consumer.Run` reconnects with jittered exponential backoff for as
long as the process is alive, and never exits or crashes the process
over RabbitMQ being down — see its doc comment. `GET /health` reports
current connectivity truthfully (`200`/`503`, same pattern as
ledger-service's Postgres check — see that endpoint's doc comment for
why this is the right choice here despite wallet-service's Redis check
always returning `200`: RabbitMQ is this service's *only* reason to
exist, not an optional latency optimization). A `503` here is a signal
to stop routing new traffic to this replica, not a signal to restart
it — restarting wouldn't fix a RabbitMQ outage that the built-in
reconnect loop wasn't already going to recover from on its own.

## Running the service

```bash
go run ./cmd/notification-service
```

Nothing needs to be running first: RabbitMQ being unreachable doesn't
stop this service from starting, only from having anything to stream
yet (see above).

### Environment variables

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `8082` | |
| `NOTIFICATION_RABBITMQ_URL` | `amqp://guest:guest@localhost:5672/` | Full AMQP URI. Never required to be set — see "Degrading sensibly" above. |

## API

### `GET /health`

```bash
curl localhost:8082/health
```

`200 OK` when connected to RabbitMQ, `503 Service Unavailable`
otherwise:

```json
{ "status": "ok" }
```

### `GET /events` — live activity feed (SSE)

```bash
curl -N localhost:8082/events
```

Streams one Server-Sent Event per transaction posted, indefinitely,
until the client disconnects. Each event's `data` is the exact JSON
ledger-service published (see its README's "Events" section for the
full shape):

```
event: transaction.posted
data: {"transaction_id":"a001...","transaction_type":"transfer","description":"optional","legs":[{"account_id":"...","direction":"debit","amount":500},{"account_id":"...","direction":"credit","amount":500}],"created_at":"2026-09-08T09:00:02Z"}

```

A `: keepalive` comment line is sent every 15 seconds during quiet
periods, so an idle proxy or load balancer between the browser and this
service doesn't time out the connection — browsers' `EventSource`
ignores comment lines, so this never reaches application code on the
frontend.

## Running tests

```bash
go test ./...
```

`internal/stream`'s tests are pure unit tests (no external dependency).
`internal/handler`'s tests use a scripted fake `Pinger` for `/health`
and a real `net/http` server (via `httptest.NewServer`) for `/events`,
since a streaming handler and `httptest.NewRecorder()` don't mix safely
across goroutines. `internal/events`'s tests run against a real,
disposable RabbitMQ container via Docker — including one
(`TestConsumer_ReconnectsAfterBrokerRestart`) that genuinely removes
and recreates the container mid-test to prove the reconnect-with-backoff
behavior, not just assert it exists — skipping gracefully if Docker
isn't available, the same convention as `ledger-service`'s and
`wallet-service`'s own real-dependency tests.
