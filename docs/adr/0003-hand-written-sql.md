# 0003 — pgx and hand-written SQL, not an ORM

**Status:** Accepted

## Context

The existing scaffold used GORM. The obvious path was to keep it.

## Decision

`pgx` with hand-written SQL in the repository adapters.

## Why

**The hot query is not CRUD.** The ticket list filters on status, priority, kind,
team, assignee, tags, full-text and SLA state, sorts five ways, and paginates by
keyset. Expressing that through an ORM's query builder is longer than the SQL
and harder to read, and it hides which index is being used.

**Postgres-specific features are the point.** `tsvector` with weighted
`setweight`, `websearch_to_tsquery`, GIN indexes on arrays, `percentile_cont`
inside a `FILTER`, `generate_series` for gap-filling a time series. Every one is
either awkward or impossible through an ORM's abstraction, and reaching for raw
SQL for the interesting half means carrying an ORM for the boring half.

**Tenant scoping should be visible.** Every query starts
`WHERE organization_id = $1`. An ORM's implicit scoping is easier to write and
harder to audit — and this is the control whose failure is most expensive.

**No hidden N+1.** An ORM will lazily load a relation inside a loop and nothing
looks wrong until production. Hand-written SQL cannot do that by accident.

## What this costs

- Substantially more code. Roughly 1,200 lines of repository against maybe 300
  with GORM.
- Every column appears three times per entity — insert, update, scan — and
  adding one means touching all three. `scanTicket` is shared between the
  single-row and multi-row paths precisely because duplicating a 29-column scan
  is how a new column ends up populated in one path and silently zero in the
  other.
- No free migrations. A small embedded migrator was written instead.

## Considered

**sqlc** — generates type-safe Go from SQL and would remove the scan
boilerplate. Genuinely appealing, and the honest reason it was not used is that
it adds a code-generation step to the build for a project of this size. Worth
adopting if the repository layer grows much further.

**Ent** — a schema-first ORM with real type safety, but its generated types
would end up in the domain layer, which is exactly what the dependency rule
forbids.

## Revisit if

The repository layer grows past roughly twice its current size. At that point
sqlc's generation step pays for itself.
