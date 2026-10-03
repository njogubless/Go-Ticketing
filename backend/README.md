# Backend

Go service: REST API, WebSocket fan-out, SLA breach worker.

## Running

```bash
make db            # Postgres in Docker
go run ./cmd/api   # migrations run automatically at startup
go run ./cmd/seed --tickets 180
```

Configuration comes from the environment; see [`.env.example`](.env.example).
It is validated at startup, and several values are refused outright in
production — a warning in a log nobody reads is not a control.

## Layout

```
cmd/api                 composition root; the only file naming concrete types
cmd/seed                dev-only demo data

internal/domain         entities, invariants, the state machine   ← stdlib only
  shared/               errors, ids, clock, pagination
  tenant/               organisation — the isolation boundary
  identity/             users, teams, roles, the permission matrix
  ticket/               the central aggregate + state machine
  sla/                  policies and business-hours calendars
  asset/                configuration items
  approval/             the change-approval gate

internal/app            use cases and ports (interfaces)          ← imports domain
internal/adapter        http, postgres, realtime, eventbus, notifier
internal/platform       config, logging, database, security
migrations              embedded SQL
```

Verify the layering holds:

```bash
# Prints nothing if the dependency rule is intact.
go list -deps ./internal/domain/... | grep -E 'gin-gonic|pgx|gorilla'
```

## Tests

```bash
go test ./... -count=1
go test ./... -race -count=1
```

They are concentrated where the risk is, not spread for coverage:

| Package | What it holds down |
|---|---|
| `domain/ticket` | Every legal and illegal transition; the guards; that `AllowedFrom` agrees with `CanTransition` for every kind/status/context; SLA pause and resume arithmetic |
| `domain/sla` | Business hours across weekends, holidays, split shifts and a DST transition; policy selection determinism |
| `domain/identity` | The permission matrix — each assertion is a line of the authorisation spec |
| `app` | That the list filter and the per-ticket access check give the same answer for every actor/ticket pair |
| `adapter/realtime` | That WebSocket fan-out enforces the same visibility as the REST API |
| `adapter/postgres` | That the SQL definition of "breaching" matches the domain's |
| `platform/security` | `alg:none`, cross-key forgery, expiry, scope confusion, bcrypt rehashing |

## Notes for the next person

**The migrator is embedded and takes an advisory lock.** Several instances
starting at once — which is what a rolling deploy is — cannot race. Each file
runs in its own transaction, so a failure leaves nothing half-applied.

**Optimistic locking has a trap.** Service methods that mutate a ticket before
calling `Update` must capture `UpdatedAt` *before* mutating. Passing the
post-mutation value compares the new timestamp against the stored old one and
never matches. Every such site names the capture `persistedUpdatedAt`; that name
is the review signal. See [ADR 0008](../docs/adr/0008-optimistic-locking.md).

**The breach predicate exists in two languages.** `ticket.Status.SLAClockRunning`
in Go and `breachingPredicate` in SQL. `breach_predicate_test.go` holds them
together — they had already diverged, in both directions, and neither divergence
raised an error anywhere. The numbers were simply wrong.

**The audit log cannot be updated.** The database rejects it. If you find
yourself wanting to correct an audit entry, append a new one.
