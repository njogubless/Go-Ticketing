# 0009 — ECS on Fargate, behind an ALB, managed by a reusable Terraform module

**Status:** Accepted

## Context

The service needed somewhere to run that is not a laptop. The constraints come
from the code, not from preference:

- **Long-lived WebSockets.** `/api/v1/realtime` holds connections open, pinged
  every 45s.
- **Background work in-process.** The SLA breach sweeper and token pruner are
  goroutines; there is no separate worker to schedule.
- **Exactly one replica.** Realtime fan-out is in-process (ADR 0004), so a
  second replica silently splits it. Whatever runs this must be content with
  one task and still deploy without downtime.
- **Migrations at startup**, behind an advisory lock, before the HTTP server
  listens.
- The same platform has to fit a FastAPI, two Django/DRF and another Go service
  later, without a per-app redesign.

## Decision

ECS on Fargate, one service per app, behind an Application Load Balancer, with
RDS PostgreSQL. All of it is a Terraform module (`infra/modules/containerized-service`)
called by a thin per-app config (`infra/envs/<app>`).

## Why not the alternatives

- **App Runner** — the closest in simplicity, and it would have been chosen if
  not for WebSockets, which it does not support. That alone rules it out here.
- **Lambda** — no long-lived connections, no in-process background goroutines,
  and the event bus assumes a process that outlives a request.
- **EKS** — a control plane that costs more per month than this entire
  environment, plus a cluster to upgrade, for one container. The orchestration
  power is real and unused.
- **EC2 + docker compose** — cheapest on paper, but health-gated rolling
  deploys, restarts and OS patching all become ours to build. The deploy safety
  this ADR is partly about would be hand-rolled.
- **Elastic Beanstalk** — workable, but it hides the pieces (the ALB, the
  target group, the deployment policy) that the rollback story needs to reason
  about explicitly.

## Why the ALB checks `/healthz`, not `/readyz`

Counter-intuitive, so it is written down. In ECS a failed load-balancer health
check does not just stop traffic — it **replaces the task**. If the ALB probed
`/readyz` (which pings Postgres), a database blip or a single-AZ maintenance
reboot would mark every task unhealthy, ECS would kill them all, and the
replacements would fail to start because the database is still down. The
restart storm the comment in `router.go` warns about, caused by infrastructure.

`/healthz` is still a strong deployment gate, because of the startup order in
`cmd/api/main.go`: config validation → database connect → migrations → *then*
the server listens. A task that answers `/healthz` at all has already proven it
can reach the database and that the schema is current. `/readyz` is probed by
`verify-deploy.sh` after each rollout, through the ALB, as the end-to-end check.

## Module boundary

The module owns everything an app needs that is not the app: network, ALB,
ECR, ECS, RDS, secrets, IAM, the 5xx alarm. The per-app config supplies only
the port, sizing, environment variables, secret names and database name. A
second app is a copied directory and about fifteen changed lines. Options that
exist for the second app — an existing VPC, a shared database, private subnets
with NAT — are in the module but defaulted off; they were added only where the
go-ticketing case needed the branch to exist cleanly, not speculatively.

## Cost being accepted

The lean shape (public-subnet tasks with no NAT gateway, single-AZ
`db.t4g.micro`, one task) is roughly **$45–55/month** in eu-west-1. That is an
estimate from list prices: ALB ~$18, RDS ~$15 with storage, Fargate ~$9, public
IPv4 addresses ~$11, Secrets Manager and logs a few dollars. It has not been
checked against a real bill.

What that buys and gives up:

- Tasks have public IPs. They are unreachable except from the ALB's security
  group, but they *have* them — a private subnet is the stronger posture, at
  ~$32/month for a NAT gateway. One variable flips it.
- Single-AZ RDS: an AZ outage or a maintenance reboot is downtime. Automated
  backups give point-in-time recovery; they do not give availability.
- No autoscaling, deliberately: ADR 0004 caps this at one replica.

## Revisit if

- Realtime moves to Redis pub/sub (ADR 0004's upgrade). Then `desired_count`
  can rise, and autoscaling and Multi-AZ become worth their cost.
- Several apps share this platform. A shared ALB (host-based routing) and a
  shared RDS instance start to beat one of each per app — the module already
  accepts an existing VPC and database for that.
- The bill matters more than the five minutes of operational work an EC2 host
  would add.
