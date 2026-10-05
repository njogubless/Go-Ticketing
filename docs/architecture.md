# Architecture

## The dependency rule

```
        ┌─────────────────────────────────────────────┐
        │  adapter/   http · postgres · realtime      │  frameworks live here
        │             eventbus · notifier             │
        └──────────────────┬──────────────────────────┘
                           │ implements
        ┌──────────────────▼──────────────────────────┐
        │  app/       use cases + ports (interfaces)  │  orchestration
        └──────────────────┬──────────────────────────┘
                           │ uses
        ┌──────────────────▼──────────────────────────┐
        │  domain/    entities · invariants · rules   │  stdlib only
        └─────────────────────────────────────────────┘

        cmd/api/main.go — the one file that names concrete types
```

Source dependencies point inward. `domain` imports nothing outside the standard
library (plus `uuid`); `app` imports `domain` and declares everything it needs
from the outside world as an interface; adapters implement those interfaces.

This is checkable rather than aspirational:

```bash
# Should print nothing. If it prints anything, a layering rule has been broken.
cd backend && go list -deps ./internal/domain/... | grep -E 'gin-gonic|pgx|gorilla'
```

The practical consequence is that the interesting logic is testable without
infrastructure. The state machine, the authorisation policy and the
business-hours calculator have no database in their tests because they have no
database in their imports.

---

## Multi-tenancy

Every tenant-owned table carries `organization_id`, and every composite index
leads with it.

The isolation is structural, not procedural:

1. **The organisation comes from the access token.** `identity.Actor` carries
   `OrgID`, reconstructed from the JWT on every request. No handler reads an
   organisation from a request body or URL, so a caller cannot address another
   tenant's data — the tenant is not an input they control.

2. **Every repository read takes `orgID` as its first parameter.** This is
   enforced by the port interfaces in `app/ports.go`: a method that omitted it
   would not satisfy its interface and would not compile.

3. **Every query binds it as `$1`.** The convention is uniform, so a `WHERE`
   clause that does not start with `organization_id = $1` is visible in review.

4. **A denied cross-tenant read returns 404.** `authoriseTicketAccess` asserts
   the organisation matches even though the repository has already scoped the
   query — the assertion costs nothing and the invariant it guards is the most
   expensive one in the system to get wrong.

The one deliberate exception is `UserRepository.ByEmail`, which spans
organisations because login happens before the tenant is known. That is why
email is globally unique rather than unique per tenant.

---

## Row-level authorisation

Two functions, both in [`app/scope.go`](../backend/internal/app/scope.go):

- `ticketScopeFor(actor)` — turns an actor into a query filter, for lists.
- `authoriseTicketAccess(actor, ticket)` — checks one loaded ticket, for reads
  and writes.

**They must agree.** A ticket that would not appear in a list must not be
reachable by guessing its ID. `TestScopeAndAccessCheckAgree` asserts exactly
that, for every combination of actor and ticket, by keeping a Go twin of the
SQL `WHERE` clause. It is duplication, and it is the duplication that turns a
silent data leak into a failing unit test that needs no database.

The policy itself:

| Actor | Sees |
|---|---|
| Requester | Only tickets they raised. Never internal notes. |
| Agent | Their team's queue, plus unrouted tickets, plus anything assigned to them personally, plus their own. |
| Manager / Admin | The whole organisation. |

Agents see unrouted tickets because otherwise a brand-new ticket is invisible to
everyone until someone routes it — which is precisely the "falls through the
cracks" failure the system exists to prevent. They keep access to tickets
assigned to them after a reroute, because handing a ticket over should not erase
the previous owner's ability to answer questions about their own work.

The realtime hub has a **third** copy of this rule, `visibleTo` in
`adapter/realtime/hub.go`, working from an event envelope rather than a loaded
ticket because the fan-out path must not touch the database. It is tested
case-by-case against the same matrix. A divergence there is the worst kind of
leak: nothing logs it, and no request appears anywhere.

---

## The ticket lifecycle

```
                        ┌──────────┐
                        │   new    │
                        └────┬─────┘
                             ▼
                       ┌───────────┐
              ┌────────│  triaged  │◄─────────────┐
              │        └─────┬─────┘              │ reopen
     change   │              │ incident /         │
     only     │              │ request / problem  │
              ▼              ▼                    │
    ┌──────────────────┐  ┌─────────────┐         │
    │ pending_approval │─►│ in_progress │─────────┤
    └────────┬─────────┘  └──────┬──────┘         │
             │ rejected           │ ▲             │
             │                    ▼ │             │
             │         ┌────────────────────┐     │
             │         │ pending_requester  │     │
             │         └──────────┬─────────┘     │
             │                    ▼               │
             │             ┌────────────┐         │
             │             │  resolved  │─────────┘
             │             └──────┬─────┘
             ▼                    ▼
        ┌───────────┐        ┌──────────┐
        │ cancelled │        │  closed  │──── reopen ──┐
        └───────────┘        └──────────┘              │
          (terminal)                                   └──► triaged
```

The table lives in `domain/ticket/statemachine.go`, keyed by `(kind, from)`, with
guard conditions on the edges:

- `in_progress` requires an assignee — you cannot be working on something nobody
  owns.
- `resolved` requires a resolution note — a resolved ticket with no explanation
  is how a queue loses its institutional memory.
- `in_progress` from `pending_approval` requires every approval granted.

`AllowedFrom` evaluates the same table speculatively and is returned on every
ticket read, so the UI renders exactly the buttons the server will accept.
`TestAllowedFrom_MatchesCanTransition` holds the two in agreement across every
kind, status and guard combination.

`cancelled` is a dead end by design: it means "no work happened", and allowing a
reopen would blur that against `closed`, which means "work happened and is
done".

---

## SLA accounting

Three moving parts:

**Calendars** define working hours as weekday windows in an IANA timezone, plus
holidays. Windows are built from `(year, month, day) + minute offset` rather than
by adding a duration to midnight — which is what makes the arithmetic correct
across daylight-saving transitions, where a day may have 23 or 25 hours but
09:00–17:00 local is still eight working hours.

**Policies** match on priority, kind and team, and carry budgets of *working*
time. Selection is by specificity, ties broken by name, so the outcome is stable
across restarts and replicas — an agent has to be able to explain why a ticket
got the deadline it got. An unmatched ticket gets no SLA rather than an invented
one; a fabricated deadline would make every attainment figure fiction.

**The clock** pauses in `pending_requester` and `pending_approval`, and on
resume both deadlines shift forward by exactly the paused span. The pause is
derived from the `(from, to)` status pair rather than from the destination alone,
so adjacent paused states cannot double-count.

Deadlines are stored as absolute instants, computed once at creation from the
policy's business-minutes budget. That is what makes "everything breaching in
the next hour" an indexed range scan instead of a full table scan with
per-row calendar arithmetic.

The breach worker queries for work on an interval rather than scheduling a timer
per ticket — a million pending timers is a memory leak with a scheduler attached.
It is idempotent via `breach_notified_at`, so a crash mid-batch re-processes
safely and nobody is alerted twice.

---

## Data model

```
organizations ─┬─ teams ──── users
               │              │
               ├─ sla_calendars ── sla_policies
               │
               ├─ assets ──┐
               │           │
               └─ tickets ─┼─ ticket_assets
                           ├─ ticket_messages ── attachments
                           ├─ approvals
                           ├─ audit_log        (append-only)
                           └─ saved_views
```

Decisions worth stating:

**UUIDv7 primary keys.** Time-ordered, so they index like a sequential key with
no B-tree page splits on insert and good locality for "recent tickets" scans,
while staying unguessable in customer-facing URLs. It is also what makes keyset
pagination work: one index serves both the ordering and the cursor.

**Keyset pagination, not offset.** `OFFSET 10000` still walks 10,000 rows, and it
skips or duplicates rows when tickets are created while an agent pages through.
Repositories over-fetch by one row to detect the next page, avoiding a `COUNT`
over a filtered ticket table — the single most expensive thing a list endpoint
can do.

**`TEXT` + `CHECK` rather than Postgres `ENUM`.** Adding a value to an enum type
is a DDL migration that cannot run inside some transactions; changing a check
constraint is trivial.

**A stored generated column for full-text search**, weighted so a subject match
outranks a body match. Generated rather than trigger-maintained, so Postgres
keeps it correct by construction and it cannot drift when a migration or a
manual fix bypasses the trigger.

**A partial index for the breach sweep**, whose predicate mirrors the worker's
query exactly. If the two drift, nothing errors — the query silently degrades to
a sequential scan, and the only symptom is that the sweep gets slower every
month. `TestBreachSweepMatchesItsIndex` guards it.

**The audit log is append-only in the database.** A trigger rejects UPDATE and
DELETE outright, and the privilege is revoked from the application role. A
separate privileged function exists for retention purges, because regulators
require records be kept and privacy law requires they not be kept forever.

---

## Request lifecycle

```
request
  → RequestID          correlation id, echoed in the response and every log line
  → Recovery           a panic becomes a 500, not a dead process
  → Logging            one structured line, including the acting user
  → SecurityHeaders    nosniff, DENY, no-store, restrictive CSP
  → CORS               explicit allowlist; no wildcard branch exists
  → MaxBodySize        1 MiB — the JSON decoder would happily buffer more
  → Authenticate       token → identity.Actor on the context
  → RateLimit          per user when known, per IP otherwise
  → RequirePermission  defence in depth, and route-table documentation
  → handler            parse and map only
      → use case       authorise, orchestrate, transact
          → domain     decide
```

Handlers contain no business rules. They parse, call one use case, and map the
result. Every enum is parsed at the boundary, so the inner layers work with
types that are valid by construction.

Errors carry a `Kind`, mapped to a status in exactly one place. Handlers that
choose their own status codes are how an API ends up returning 400 for a
permission failure on one endpoint and 403 on another. The cause chain is logged
but never serialised: a constraint name in a response body tells an attacker
about the schema and tells a legitimate caller nothing they can act on.

---

## Concurrency

**Optimistic locking on ticket writes.** Two agents opening the same ticket is
routine on a busy desk, and last-write-wins silently discards one of them —
including, potentially, a resolution note. The client echoes the `ETag` from its
last read as `If-Match`; a stale write matches zero rows and returns 409.

An `ETag` carrying `updated_at` at nanosecond precision, rather than the more
idiomatic `Last-Modified`/`If-Unmodified-Since` pair, because that pair's time
format is only accurate to the second while `updated_at` is stored to the
microsecond — every conditional write would fail as a false conflict.

**Transactions propagate on the context.** Repositories join an ambient
transaction without their signatures mentioning transactions at all. Nested
`WithinTx` calls join the outer one rather than opening a second, so a service
calling another service cannot commit half of its caller's work.

**Events are published after commit, never inside it.** A publish failure must
not fail a request whose write is already durable — a dropped notification is a
lesser harm than a 500 on a write that succeeded.

**Handlers run on the bus's own goroutines**, so a slow mail server cannot slow
down the service desk, and each handler is panic-isolated so one bad subscriber
cannot take down the worker and every event behind it.
