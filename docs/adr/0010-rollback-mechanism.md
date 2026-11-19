# 0010 — Rollback: fail-closed rolling deploys, automatic revert, revision pinning

**Status:** Accepted, not yet exercised against real AWS

## Context

"How do you roll back?" needs a concrete answer: what happens on its own when a
deploy goes bad, and what a human runs when the automatic part was not enough.

## Decision

Four layers, each catching what the one before cannot, plus a reconciliation
step.

| # | Layer | Catches | Who acts |
|---|---|---|---|
| 1 | Rolling deploy, `minimum_healthy_percent = 100` | Anything that stops a new task becoming healthy — the old task is never stopped first | Nobody; it simply never cuts over |
| 2 | ECS deployment circuit breaker, `rollback = true` | Crash loops, failing `/healthz`, image pull failures, missing secrets | ECS, automatically |
| 3 | CloudWatch alarm on target 5xx, attached to the deployment | A revision that is healthy but returning errors under traffic, *during* the rollout | ECS, automatically |
| 4 | `make rollback` (`infra/scripts/rollback.sh`) | A bad-but-healthy revision found *after* the rollout — a logic bug | A human, one command |
| — | Re-pin: deploy workflow with `image_tag=<good sha>` | Terraform state still naming the bad revision | A human, after layer 4 |

### What makes it work

- **Every deploy is a numbered task-definition revision.** A new `image_tag`
  (the commit SHA) produces a new revision.
- **Revisions are never deleted** (`skip_destroy = true`). Without that,
  Terraform deregisters the previous revision on every deploy — removing the
  rollback target exactly when it is needed.
- **Image tags are immutable in ECR.** Revision N always means the same bits.
- **The rollback script needs only AWS credentials.** The module names the
  cluster, service and family identically, so the script needs no Terraform,
  no state, no checkout. In an incident those are the things you want to depend
  on least.
- **A rollback is itself a health-gated rolling deploy.** It cannot cause an
  outage that the bad deploy did not.

### The script refuses to act when acting is wrong

- A deployment is already IN_PROGRESS — the circuit breaker may be rolling back
  already (`--force` overrides).
- The target revision's image has been expired from ECR by the lifecycle
  policy. The rollback horizon is `ecr_keep_images` (30) deploys.
- The target revision is not ACTIVE, or there is no older revision.

And it does not trust `aws ecs wait services-stable`, which reports success when
the service is stable on *any* revision — including the old one ECS just rolled
back to. `verify-deploy.sh` tracks the specific revision to COMPLETED or FAILED.

## What it does not cover

Stated plainly, because these are where a "rollback" quietly does nothing:

- **The database.** Migrations run forward at startup and are never reversed.
  Rolling back code after a migration means old code against the new schema.
  The rule this imposes: **migrations must be backward-compatible with the
  previous release** (expand, deploy, then contract in a later release). A
  destructive migration's only undo is RDS point-in-time restore — minutes of
  data loss and a manual procedure, not this mechanism.
- **Secret values.** Task definitions reference secrets by ARN, not by version.
  Rolling back revision 8 → 7 does not restore last week's `SMTP_PASSWORD`; the
  old code reads today's value.
- **Infrastructure changes.** A bad security-group rule or ALB setting is undone
  by `git revert` and a normal deploy, not by switching task revisions.
- **The very first deploy.** There is no previous revision to fall back to.
- **Low traffic.** The 5xx alarm needs >10 errors/minute for two minutes. A
  sandbox with no users will not trip it; layer 4 is the backstop. The alarm
  also cannot tell old tasks' errors from new ones'.

## Rollback causes Terraform drift — on purpose

After `make rollback`, ECS runs revision 7 while Terraform state names 8. The
next `terraform plan` shows the service moving back to 8. That is the honest
state of things, and it is visible. The fix is to re-pin (run the deploy
workflow with `image_tag` set to the good SHA) or fix forward. The script
prints both options. The alternative — Terraform ignoring the service's task
definition — hides real drift permanently in exchange for hiding this one
briefly.

## Why not blue/green via CodeDeploy

Blue/green's real advantage is a near-instant traffic shift back while the old
tasks are still warm. Rejected for now because:

- It adds a CodeDeploy application, a deployment group, a second target group, a
  test listener and an AppSpec, and it makes Terraform and CodeDeploy fight over
  the service (Terraform must ignore the fields CodeDeploy owns).
- With one task, rolling-with-100%-minimum already keeps the old task serving
  until the new one is healthy. The gap between the two strategies is the
  *manual* rollback time — a couple of minutes for a new task to start, against
  seconds.

## Verification status

Proven without AWS (both run in CI on every infra change):

- `check-plan.sh` asserts on the planned resources: circuit breaker with
  rollback on, minimum healthy ≥ 100%, alarm rollback wired, `skip_destroy`,
  immutable ECR tags.
- `infra/scripts/test/run.sh` runs 16 tests of the rollback and verification
  logic against a fake AWS CLI. Each guard was mutation-tested: disabling it
  makes at least one test fail.

Not proven: that real ECS responds exactly as the fixtures model. The rollback
drill in `docs/infra/README.md` closes that gap and should be run once, before
this ADR's status drops "not yet exercised".

## Revisit if

- Manual rollback time (a new task's start time) becomes unacceptable. Then
  look at blue/green — CodeDeploy, or ECS's native blue/green strategy if the
  Terraform provider's support has matured.
- More than one replica runs. Then a canary (shift a fraction of traffic first)
  becomes possible and useful.
- A migration that cannot be made backward-compatible is needed. That deploy
  needs its own runbook; this mechanism will not protect it.
