# infra/

Terraform for running the API on AWS (ECS Fargate + ALB + RDS), plus the
deploy-verification and rollback scripts.

```bash
make infra-check     # fmt, validate, plan, plan invariants, script tests — no AWS needed
make revisions       # list deployable revisions          (needs AWS credentials)
make rollback        # roll back one revision, or REVISION=N (needs AWS credentials)
```

Requires Terraform ≥ 1.10 (`make infra-plan TERRAFORM=/path/to/terraform` if
your default is older), `jq`, and the AWS CLI v2 for the rollback targets.

- [`modules/containerized-service`](modules/containerized-service) — the reusable module
- [`envs/go-ticketing`](envs/go-ticketing) — this app's configuration
- [`scripts/`](scripts) — offline plan, plan invariants, verify-deploy, rollback, tests

**Read [docs/infra/README.md](../docs/infra/README.md)** for the full
explanation, the one-time AWS setup, enabling deploys, the first apply, and
the rollback drill. Decisions: [ADR 0009](../docs/adr/0009-ecs-fargate.md),
[ADR 0010](../docs/adr/0010-rollback-mechanism.md).

Nothing here has been applied to AWS yet; CI deploy jobs are disabled until
the repository variable `DEPLOY_ENABLED` is `true`.
