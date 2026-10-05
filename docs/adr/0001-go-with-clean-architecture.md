# 0001 — Go, layered by the dependency rule

**Status:** Accepted

## Context

The reference guide for this project recommended Django/DRF or FastAPI. A
partial Go backend already existed — Gin and GORM, and not compiling: it
imported two packages that had never been written, and `routes.Register` took
three arguments where `main` passed five.

So the real question was whether to finish the Go service or restart in Python.

## Decision

Go, restructured so the layering is enforced by the compiler rather than by
convention:

```
domain/    entities, invariants, the state machine   (stdlib only)
app/       use cases and ports (interfaces)          (imports domain)
adapter/   http, postgres, realtime, eventbus, mail  (imports app)
platform/  config, logging, database, security
cmd/api    the composition root
```

## Why not Django

Django would genuinely have been faster to a feature-complete system — the admin
panel alone replaces the whole administration screen. But Django's conventions
and the dependency rule pull in opposite directions: a Django model is an active
record that knows how to persist itself, so the domain layer imports the ORM by
construction. Fighting that produces a codebase that is neither idiomatic Django
nor clean architecture.

Given that clean architecture was an explicit requirement, the framework that
resists it least is the better base.

## Why not FastAPI

FastAPI layers cleanly and would have been a reasonable choice. Go wins on three
specifics rather than in general:

- The SLA breach worker, the event bus and the WebSocket hub are all concurrent
  and long-lived. Goroutines and channels model that directly; the async
  equivalent needs a task runner and careful attention to what blocks the loop.
- A single static binary in a distroless image is a materially smaller
  deployment and attack surface than a Python runtime plus its dependency tree.
- The existing work was in Go, and it was structural rather than cosmetic —
  discarding it to end up with a comparable architecture is a poor trade.

## What this costs

- More code. Explicit DTO mapping, explicit SQL, explicit error wrapping. The
  Go backend is roughly twice the line count of the Django equivalent.
- No admin panel. The administration screen is hand-built.
- No migration framework, no serializer framework, no permission framework —
  each is a small hand-rolled equivalent instead.

## Revisit if

The system needs a large volume of admin CRUD screens with little logic behind
them. That is the case Django is unbeatable at, and the trade would flip.
