# Service Desk

An IT service management system: incidents, service requests, change requests
with approval gates, and problem records — routed to teams, measured against
business-hours SLAs, and linked to the equipment they are about.

Go backend, React/TypeScript frontend, PostgreSQL. Multi-tenant.

---

## Quick start

```bash
make up      # Postgres, API and web app in Docker
make seed    # a realistic demo desk: 180 tickets, 25 people, 40 assets
```

Then open <http://localhost:5173>.

| Role | Email | Password |
|---|---|---|
| Admin | `admin@acme.test` | `demo-password-1234` |
| Manager | `manager0@acme.test` | `demo-password-1234` |
| Agent | `agent1@acme.test` | `demo-password-1234` |
| Requester | `requester7@acme.test` | `demo-password-1234` |

Sign in as each to see how different the same system looks: the agent gets a
queue and internal notes, the requester gets their own tickets and a thread
with the internal notes removed server-side, the manager gets the whole
organisation plus reporting and the approvals inbox.

If port 5432 is already taken by a local Postgres:

```bash
POSTGRES_PORT=55432 make up
POSTGRES_PORT=55432 make seed
```

Running the pieces separately:

```bash
make db      # just Postgres
make api     # the Go service on :8080
make web     # the Vite dev server on :5173
make check   # everything CI runs
```

---

## What it does

A ticketing system turns unstructured requests into tracked work. This one is
specifically an **IT service desk**, which changes the model in four ways that a
generic helpdesk does not have:

**Priority is derived, not chosen.** Requesters answer two questions in plain
language — who is affected, and how badly — and priority falls out of an
impact × urgency matrix. When agents pick priority directly, everything becomes
High within a quarter and queue ordering stops meaning anything.

**Changes are gated behind approval.** A change request cannot move from triage
to in-progress; it must pass through `pending_approval` and collect a decision
from every nominated approver. Self-approval is rejected in the domain *and* by
a database constraint. This one rule is the difference between a helpdesk and
change management.

**SLAs are measured in business hours.** A four-hour target raised at 16:00 on a
Friday is due at 11:00 on Monday — not 20:00 that evening — and if Monday is a
public holiday it moves again. The clock stops while a ticket waits on its
requester or an approver, because that is not time the desk can be held to.
P1 incidents run on a 24/7 calendar; nothing else does.

**Tickets link to assets.** Which turns a pile of individually unremarkable
tickets into "these six laptops produced a quarter of this month's incidents" —
a purchasing conversation rather than a support one.

---

## Architecture

```
web/                     React + TypeScript
backend/
  cmd/api                composition root — the only place concrete types are named
  cmd/seed               dev-only demo data
  internal/
    domain/              entities, invariants, the state machine   (no imports out)
    app/                 use cases + ports (interfaces)            (imports domain)
    adapter/             http, postgres, realtime, eventbus, mail  (imports app)
    platform/            config, logging, database, security
  migrations/            embedded SQL
```

Dependencies point inward only. `domain` imports nothing but the standard
library; `app` imports `domain` and declares every outward capability it needs
as an interface; the adapters implement those interfaces and are wired together
in exactly one file, `cmd/api/main.go`.

The practical payoff: the domain and use-case layers compile and test with no
database, no HTTP server and no network. `go test ./internal/domain/... ./internal/app/...`
runs in milliseconds against no infrastructure at all.

Full detail, including the tenancy and authorisation model:
[docs/architecture.md](docs/architecture.md).
Decisions and their trade-offs: [docs/adr/](docs/adr/).

---

## The parts worth reading

If you only look at a few files, these are the ones carrying the design:

| File | Why |
|---|---|
| [`domain/ticket/statemachine.go`](backend/internal/domain/ticket/statemachine.go) | The lifecycle as an explicit table, with guards on the edges. Shipped to the frontend so the UI renders exactly the buttons the server accepts. |
| [`app/scope.go`](backend/internal/app/scope.go) | The whole row-level authorisation policy, in one small file — deliberately, because "who can see which ticket" should be readable in one sitting. |
| [`domain/sla/calendar.go`](backend/internal/domain/sla/calendar.go) | Business-hours arithmetic, DST-correct and holiday-aware. |
| [`adapter/postgres/ticket_repo.go`](backend/internal/adapter/postgres/ticket_repo.go) | The hot query path: keyset pagination, full-text search, the reporting aggregates. |
| [`app/ports.go`](backend/internal/app/ports.go) | Every capability the use cases need, as interfaces. The dependency rule made concrete. |

---

## Security

The controls, and what each one is actually for:

- **Tenant isolation is structural.** Every repository read takes `orgID` as its
  first parameter, so a method that omitted it would not satisfy its interface.
  The organisation comes from the access token, never from a request — a caller
  cannot address another tenant's data because the tenant is not an input they
  control.
- **Denied reads return 404, not 403.** Answering "forbidden" confirms a ticket
  exists and turns the ID space into an enumeration oracle.
- **Row-level authorisation is enforced at the query layer**, not after fetching.
  Internal notes are filtered in SQL, so they never cross a process boundary and
  no later refactor can leak them.
- **Access tokens are short-lived (15m) and stateless; refresh tokens are
  long-lived, opaque, single-use and rotating.** Presenting a consumed refresh
  token is treated as theft and revokes the entire token family.
- **The JWT algorithm is pinned**, closing the `alg:none` and RS256-confusion
  class outright. Access tokens and WebSocket handshake tickets carry different
  scopes and are not interchangeable.
- **The WebSocket handshake uses a 30-second single-purpose ticket**, because a
  browser cannot set headers on an upgrade and query strings end up in every
  proxy log.
- **The audit log is append-only in the database** — a trigger rejects UPDATE and
  DELETE, so a bug cannot rewrite history.
- **Passwords: bcrypt cost 12**, with transparent rehashing when the cost rises.
  Login burns a hash comparison even for unknown accounts, so timing does not
  distinguish them.
- **CORS is an explicit allowlist with no wildcard branch**, and the config
  refuses to start in production with `sslmode=disable`, a short JWT secret, or
  a plaintext origin.

The tests that hold these in place are in
[`app/scope_test.go`](backend/internal/app/scope_test.go),
[`realtime/hub_test.go`](backend/internal/adapter/realtime/hub_test.go) and
[`security/token_test.go`](backend/internal/platform/security/token_test.go).

---

## Testing

```bash
make test        # everything
make test-race   # with the race detector
make cover       # coverage per package
```

The suite is concentrated where the risk is: the state machine (every legal and
illegal transition, plus a test asserting the UI's button list agrees with the
server's rules), the visibility scope (a test asserting the list filter and the
per-ticket check give the same answer for every actor/ticket pair), business-hours
arithmetic including DST and holidays, and JWT hardening.

One test worth singling out:
[`breach_predicate_test.go`](backend/internal/adapter/postgres/breach_predicate_test.go)
pins the SQL definition of "breaching" to the domain's. They had already
diverged — the dashboard reported 18 breaches on data where one ticket was
breaching — and writing the test surfaced a second, opposite bug in the domain.
Neither failure raised an error anywhere; the numbers were simply wrong.

---

## What is deliberately not built

Stated plainly, because a list of known gaps is more useful than a demo that
implies completeness:

- **Inbound email → ticket.** The outbound side works; ingestion needs a provider
  webhook, a public URL, and Message-ID deduplication. It is its own project.
- **Attachments.** The domain type, validation and storage port exist; the S3
  adapter does not.
- **A distributed rate limiter.** The current one is per-instance and in-memory,
  so behind N replicas the effective limit is N×. Fine for abuse control, wrong
  for quotas — see [ADR 0007](docs/adr/0007-rate-limiting.md).
- **Refresh tokens in an httpOnly cookie.** They currently live in
  `localStorage`, which is a real trade-off rather than an oversight — see
  [ADR 0006](docs/adr/0006-token-storage.md).
- **Horizontal scale of the realtime layer.** The event bus is in-process, so a
  second replica would not see the first's events. The `EventPublisher` port is
  the seam — see [ADR 0004](docs/adr/0004-in-process-event-bus.md).
