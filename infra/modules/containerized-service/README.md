# containerized-service

One HTTP backend on ECS Fargate behind an ALB, with its own ECR repository,
optional RDS PostgreSQL, Secrets Manager secrets, scoped IAM, and a
fail-closed rolling deployment (circuit breaker + 5xx alarm rollback).

```hcl
module "api" {
  source = "../../modules/containerized-service"

  name               = "myapp-sandbox"            # names everything
  availability_zones = ["eu-west-1a", "eu-west-1b"]
  image_tag          = var.image_tag               # commit SHA, from CI

  container_port    = 8000
  health_check_path = "/healthz"
  environment       = { LOG_LEVEL = "info" }
  generated_secrets = ["SECRET_KEY"]               # random, Terraform-owned
  external_secrets  = ["SMTP_PASSWORD"]            # you set the value
  db_name           = "myapp"
}
```

The container receives `DATABASE_URL` plus every name in `generated_secrets`
and `external_secrets` as environment variables.

Contract other tooling relies on: the ECS cluster, service, task-definition
family and ECR repository are all named exactly `name`.

Every input is documented in [variables.tf](variables.tf); the full walkthrough
is [docs/infra/README.md](../../../docs/infra/README.md) §3.
