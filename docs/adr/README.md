# Architecture decision records

Short notes on decisions that were not obvious, written so the next person —
including the next version of me — can tell whether the reasoning still holds.

Each one states the decision, the alternatives that were rejected, the cost
being accepted, and the signal that should trigger a revisit. A decision with no
stated cost is usually a decision nobody examined.

| # | Decision | Status |
|---|---|---|
| [0001](0001-go-with-clean-architecture.md) | Go, layered by the dependency rule | Accepted |
| [0002](0002-multi-tenancy.md) | Multi-tenant from day one, single shared schema | Accepted |
| [0003](0003-hand-written-sql.md) | pgx and hand-written SQL, not an ORM | Accepted |
| [0004](0004-in-process-event-bus.md) | In-process event bus | Accepted, with a known ceiling |
| [0005](0005-derived-priority.md) | Priority derived from impact × urgency | Accepted |
| [0006](0006-token-storage.md) | Short access tokens, rotating refresh tokens | Accepted, with a known gap |
| [0007](0007-rate-limiting.md) | Per-instance in-memory rate limiting | Accepted, with a known ceiling |
| [0008](0008-optimistic-locking.md) | Optimistic locking via ETag | Accepted |
| [0009](0009-ecs-fargate.md) | ECS on Fargate behind an ALB, as a reusable Terraform module | Accepted |
| [0010](0010-rollback-mechanism.md) | Fail-closed rolling deploys, automatic revert, revision pinning | Accepted, not yet exercised against real AWS |
