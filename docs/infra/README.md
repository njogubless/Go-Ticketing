# Infrastructure, deployment and rollback — the complete guide

This is the long-form explanation of everything under `infra/` and the deploy
jobs in `.github/workflows/ci.yml`: what each piece is, why it is shaped that
way, what happens second by second during a deploy, and what to do when one
goes wrong. The two short decision records are
[ADR 0009](../adr/0009-ecs-fargate.md) (why ECS/Fargate) and
[ADR 0010](../adr/0010-rollback-mechanism.md) (the rollback mechanism).

**Current state:** everything is built and verified offline. Nothing has been
applied to AWS. The deploy jobs exist but are switched off.

---

## Contents

1. [Map of the files](#1-map-of-the-files)
2. [The architecture in one picture](#2-the-architecture-in-one-picture)
3. [The module, file by file](#3-the-module-file-by-file)
4. [The per-app config (envs/go-ticketing)](#4-the-per-app-config)
5. [Secrets: where every value comes from](#5-secrets)
6. [The deploy pipeline, job by job](#6-the-deploy-pipeline)
7. [Anatomy of a deploy: the timeline](#7-anatomy-of-a-deploy)
8. [When a deploy goes bad: scenarios and responses](#8-when-a-deploy-goes-bad)
9. [Rollback: the commands](#9-rollback-the-commands)
10. [Verifying without AWS](#10-verifying-without-aws)
11. [One-time AWS setup](#11-one-time-aws-setup)
12. [Enabling deploys](#12-enabling-deploys)
13. [The first apply](#13-the-first-apply)
14. [Rollback drill (run once real AWS exists)](#14-rollback-drill)
15. [Adding a second app](#15-adding-a-second-app)
16. [Assumptions, guesses and known gaps](#16-assumptions-guesses-and-known-gaps)
17. [The two-minute explanation](#17-the-two-minute-explanation)

---

## 1. Map of the files

```
infra/
├── modules/containerized-service/   ← generic; knows nothing about go-ticketing
│   ├── versions.tf      Terraform ≥1.10, aws provider 6.x, random 3.x
│   ├── variables.tf     every input, grouped, with validations
│   ├── network.tf       optional VPC, subnets, routes, NAT, 3 security groups
│   ├── alb.tf           load balancer, target group + health check, listeners
│   ├── ecr.tf           image repository (immutable tags) + lifecycle policy
│   ├── database.tf      RDS PostgreSQL + random master password
│   ├── secrets.tf       Secrets Manager: DATABASE_URL, generated, external
│   ├── iam.tf           execution role (scoped) + empty task role
│   ├── ecs.tf           log group, cluster, task definition, service, 5xx alarm
│   └── outputs.tf
├── envs/go-ticketing/               ← the thin per-app layer
│   ├── versions.tf      provider pins + empty S3 backend block
│   ├── providers.tf     region + default tags
│   ├── variables.tf     region, AZs, image_tag, SMTP, origins, cert
│   ├── main.tf          THE per-app surface: one module call
│   ├── outputs.tf       re-exports for CI and scripts
│   ├── example.tfvars   placeholder values for credential-less plans
│   ├── backend.hcl.example
│   └── .terraform.lock.hcl   provider checksums (committed, like go.sum)
└── scripts/
    ├── plan-offline.sh  fmt + validate + plan + invariants, no AWS needed
    ├── check-plan.sh    asserts deployment-safety properties on a plan
    ├── verify-deploy.sh waits for ONE revision to COMPLETE or FAIL
    ├── rollback.sh      switch the service to an earlier revision
    └── test/
        ├── run.sh       16 tests of rollback.sh + verify-deploy.sh
        └── fake-aws     stand-in AWS CLI that serves canned JSON
```

Makefile targets (`make help` lists them): `infra-fmt`, `infra-plan`,
`infra-test`, `infra-check`, `revisions`, `rollback`.

---

## 2. The architecture in one picture

```
                         Internet
                            │  :80 (and :443 once certificate_arn is set)
                            ▼
      ┌──────────────── VPC 10.40.0.0/16 ─────────────────────────────┐
      │                                                               │
      │  public subnets 10.40.0.0/24 (AZ a), 10.40.1.0/24 (AZ b)      │
      │  ┌──────────────────────────────┐                             │
      │  │ ALB  (sg: alb)               │  health check: GET /healthz │
      │  │ listener :80 → target group  │  every 10s, 2 ok = healthy  │
      │  └──────────────┬───────────────┘                             │
      │                 │ :8080 only from sg:alb                      │
      │  ┌──────────────▼───────────────┐                             │
      │  │ ECS Fargate task (sg: service)│ public IP, but no inbound  │
      │  │ container "app": the Go API   │ except from the ALB        │
      │  └──┬───────────────┬───────────┘                             │
      │     │ :5432         │ :443 out → ECR, Secrets Manager, Logs   │
      │     │               │ :587 out → SMTP relay                   │
      │  private subnets 10.40.10.0/24, 10.40.11.0/24 (no internet)   │
      │  ┌──▼───────────────────────────┐                             │
      │  │ RDS PostgreSQL 16 (sg: db)   │ not publicly accessible     │
      │  └──────────────────────────────┘                             │
      └───────────────────────────────────────────────────────────────┘

  Outside the VPC (regional AWS services):
    ECR repo  go-ticketing-sandbox        images tagged by commit SHA
    Secrets   go-ticketing-sandbox/DATABASE_URL, /JWT_SECRET, /SMTP_PASSWORD
    Logs      /ecs/go-ticketing-sandbox
    Alarm     go-ticketing-sandbox-target-5xx  → wired to the ECS deployment
```

Every resource is named `go-ticketing-<environment>` (sandbox today). The
cluster, service, task-definition family and ECR repository all share that
exact name. The rollback script depends on this.

---

## 3. The module, file by file

### versions.tf

Pins Terraform `>= 1.10` and the AWS provider to `6.x`. 1.10 is the minimum
because of two features used here:

- **cross-variable validation** (1.9+). For example, `create_nat_gateway`'s
  validation reads `var.tasks_in_private_subnets`.
- **S3-native state locking** (`use_lockfile`, 1.10+), so there's no DynamoDB
  lock table.

The module declares providers but never configures them. Region and
credentials belong to the caller, so the same module works in any account or
region.

### variables.tf

Every input, grouped by concern, with a description and usually a safe
default. Validations that matter:

| Validation | Why |
|---|---|
| `name` is 3–24 lowercase chars | ALB and target group names max out at 32 chars; the module appends up to 6 (`-final`). |
| `image_tag` is not `latest` and is a valid tag | With `latest`, "revision N" would mean different code at different times, and revision-based rollback stops meaning anything. |
| ≥2 `availability_zones` when creating a VPC | RDS subnet groups and ALBs both require two AZs. |
| private-subnet tasks need a NAT | Without a route out, Fargate can't pull the image, and every task dies with an opaque `ResourceInitializationError`. Catching it at plan time is cheaper. |
| `existing_*` set when `create_vpc`/`create_database` is false | Fail at plan time, not halfway through apply. |

Why AZs are an input instead of a `data "aws_availability_zones"` lookup: the
lookup calls AWS, and then `terraform plan` can't run without credentials. The
whole module avoids data sources that call AWS APIs. The only data sources are
`aws_iam_policy_document` (pure JSON rendering) and `aws_region` (read from the
provider config). Both are local.

### network.tf

**VPC** (only when `create_vpc = true`):

- `aws_vpc`, with DNS support and hostnames on. Both are needed for the RDS
  endpoint and AWS API hostnames to resolve.
- `aws_default_security_group` with no rules. Every VPC comes with a default
  group that lets members talk to each other freely. Adopting it with zero
  rules neuters it.
- Public subnets come from `cidrsubnet(vpc_cidr, 8, i)`: `10.40.0.0/24`,
  `10.40.1.0/24`. Private subnets use `i + 10`: `10.40.10.0/24`,
  `10.40.11.0/24`. The offset means adding AZs never makes the two ranges
  collide.
- The public route table sends `0.0.0.0/0` to the internet gateway.
- The private route table has **no default route** unless a NAT exists, so
  the database subnets can't reach the internet at all.
- An optional single NAT gateway in the first public subnet (one, not one per
  AZ: a third of the cost, accepted single-AZ egress risk).
- `map_public_ip_on_launch = false`: public IPs are assigned per ECS service
  explicitly, never by accident.

**Locals:** `local.vpc_id`, `local.public_subnet_ids`, `local.private_subnet_ids`
and `local.task_subnet_ids` are the only things the other files read. They
point at either the created resources or the `existing_*` inputs. That is why
"bring your own VPC" touches one file.

**Security groups:** a chain of three, each admitting only the one before it:

```
internet ──80/443──▶ alb ──8080──▶ service ──5432──▶ db
```

| Rule | Purpose |
|---|---|
| alb ingress 80 (+443 with cert) from `alb_ingress_cidrs` | public entry |
| alb egress 8080 → service SG | the ALB can only talk to tasks |
| service ingress 8080 ← alb SG | tasks unreachable except via the ALB, despite their public IP |
| service egress 443 → 0.0.0.0/0 | ECR, Secrets Manager, CloudWatch are public AWS endpoints |
| service egress `extra_egress_ports` (587) | SMTP for notification email |
| service egress 5432 → db SG | database |
| db ingress 5432 ← service SG | the only way into RDS |

Rules are separate `aws_vpc_security_group_ingress_rule`/`egress_rule`
resources, not inline blocks. Two reasons: mixing inline and standalone rules
on one group makes them overwrite each other, and standalone rules can
reference each other's groups without a dependency cycle (alb → service →
alb).

DNS needs no rule: traffic to the VPC resolver isn't filtered by security
groups.

### alb.tf

- `aws_lb`: internet-facing, in the public subnets.
  - `idle_timeout = 120`. The app pings WebSockets every 45s, so the 60s
    default would just about work; 120 leaves headroom.
  - `drop_invalid_header_fields = true` rejects malformed headers (a
    request-smuggling defence).
- `aws_lb_target_group`: `target_type = "ip"` because Fargate tasks get their
  own network interfaces (awsvpc mode). Health check: `GET /healthz`,
  expecting 200, every 10s, 5s timeout. A target is healthy after 2 passes
  (~20s) and pulled after 3 failures (~30s). `deregistration_delay = 30`:
  in-flight requests get 30s to finish before ECS stops the task.
- `aws_lb_listener.http` (port 80): one resource whose action is `forward`
  with no certificate and `redirect → 443` once there is one. Adding TLS is an
  in-place change, not a destroy-and-recreate.
- `aws_lb_listener.https`: only exists with a certificate, and uses the
  TLS 1.3/1.2 policy.

**Why `/healthz` rather than `/readyz`.** This is the most important subtle
decision in the module, explained in full in ADR 0009. In short: in ECS a
failed ALB health check *replaces the task*. Probing `/readyz` (which pings the
database) would turn a database blip into ECS killing every task: the restart
storm the comment in `router.go` warns about. `/healthz` is still a strong
gate, because `main.go` connects to the database and runs migrations *before*
it starts listening. If `/healthz` answers at all, the database was reachable
and the schema is current.

### ecr.tf

- `image_tag_mutability = "IMMUTABLE"`: once `abc123` is pushed, that tag can
  never point at different bits. This is what makes a task-definition revision
  a stable rollback target.
- `scan_on_push = true`: basic CVE scan on every image.
- Lifecycle policy:
  - Rule 1 expires untagged images after 7 days (debris from failed pushes).
  - Rule 2 keeps the newest 30 images.
  - **30 is the rollback horizon.** A revision whose image has been expired
    can't start, which is why `rollback.sh` checks the image exists before
    switching.

### database.tf

- `random_password` (32 alphanumerics, ~190 bits). No special characters,
  because the password is embedded in a URL and RDS also forbids `/ @ "` and
  spaces.
- `aws_db_subnet_group` over the private subnets.
- `aws_db_instance`:

| Setting | Value | Why |
|---|---|---|
| engine / version | postgres `16` | matches docker-compose and CI (`postgres:16-alpine`) |
| class | `db.t4g.micro` | cheapest Graviton instance; ~80 max connections |
| storage | 20 GiB gp3, autoscaling to 100, encrypted | gp3 is cheaper and faster than gp2 |
| `publicly_accessible` | false | no public endpoint at all |
| `multi_az` | false | lean sandbox; true doubles cost |
| backups | 7 days | point-in-time restore is the only undo for a destructive migration |
| `auto_minor_version_upgrade` | true | security patches in the maintenance window |
| `apply_immediately` | false | Terraform changes wait for the window rather than restarting the DB mid-day |
| `deletion_protection` | true | `terraform destroy` can't delete it until you flip this |
| `final_snapshot_identifier` | `<name>-final` | even a deliberate delete keeps a snapshot |

RDS for PostgreSQL 15+ enforces SSL by default (`rds.force_ssl = 1`), which is
why the URL says `sslmode=require` (see §5).

### secrets.tf

Three kinds of secret, all named `<name>/<ENV_VAR>`:

1. **`DATABASE_URL`**: built by Terraform from the RDS endpoint and password:
   `postgres://ticketing:<pw>@<host>:5432/ticketing?sslmode=require`.
2. **`generated_secrets`** (go-ticketing: `JWT_SECRET`): 48 random
   alphanumerics, comfortably above the API's 32-char minimum.
3. **`external_secrets`** (go-ticketing: `SMTP_PASSWORD`): values only a human
   knows. Terraform creates the secret with the value `REPLACE_ME`, and
   `lifecycle { ignore_changes = [secret_string] }` means it never overwrites
   what you put there later. Why the placeholder: an ECS secret with *no*
   value makes every task fail to start. With a placeholder the API starts,
   and only email fails until you set the real password.

`local.secret_arns` (env var name → ARN) feeds both the task definition's
`secrets` list and the execution role's IAM policy, so those two can't drift
apart.

`recovery_window_in_days = 7`: a deleted secret can be restored for a week.
Side effect: re-creating a secret with the same name inside that week fails.
For sandboxes you tear down repeatedly, set `secret_recovery_window_days = 0`.

### iam.tf

ECS involves two principals, so there are two roles:

- **Execution role**: used by the ECS agent *before* your code runs, to pull
  the image, read secrets and create the log stream. Its inline policy grants:
  - `ecr:GetAuthorizationToken` on `*` (IAM doesn't support scoping this
    action; the token alone grants nothing).
  - Pull actions on **this repository only**.
  - `logs:CreateLogStream`/`PutLogEvents` on **this log group only**.
  - `secretsmanager:GetSecretValue` on **these secrets only**.

  The AWS-managed `AmazonECSTaskExecutionRolePolicy` is deliberately not used:
  it grants pull on every repository and logs on every group in the account.
- **Task role**: the identity your code runs as. **No permissions**, because
  go-ticketing calls no AWS APIs. A future app that needs S3 attaches a policy
  to it from its own `envs/` directory (output `task_role_name`).

### ecs.tf

- **Log group** `/ecs/<name>`, 30-day retention. The app logs JSON
  (`LOG_FORMAT=json`), so CloudWatch Logs Insights can query fields.
- **Cluster**, with Container Insights off (extra cost; ALB metrics cover the
  alarms).
- **Task definition** (family `<name>`):
  - Fargate, `awsvpc`, 0.25 vCPU / 512 MiB, `X86_64` (CI builds on amd64
    runners).
  - One container, `app`, image `<ecr-url>:<image_tag>`.
  - `environment` and `secrets` are built from **sorted** keys. Without the
    sort, reordering a map in tfvars would change the JSON, which means a new
    revision and a pointless deploy.
  - `readonlyRootFilesystem = true`: the Go binary writes nothing to disk.
  - `stopTimeout = 30`: the API's graceful shutdown waits up to 20s, and
    SIGKILL has to come after that.
  - **`skip_destroy = true`**. By default, when Terraform registers revision
    8 it *deregisters* revision 7. This flag keeps every revision, which is
    what makes them rollback targets.
  - Two **preconditions**: a name can't be both a plain env var and a secret,
    and secret names can't collide (or include `DATABASE_URL`, which the
    module owns).
- **Service**:
  - `desired_count = 1`, Fargate, in `local.task_subnet_ids`, with
    `assign_public_ip = true` in the lean posture (a public IP is how a
    public-subnet task reaches ECR without a NAT).
  - `deployment_minimum_healthy_percent = 100`, `maximum_percent = 200`. With
    one task this means: start the new task alongside the old one, wait until
    it's healthy, then drain and stop the old one. The old task is never
    stopped first, so the deploy **fails closed**.
  - `deployment_circuit_breaker { enable = true, rollback = true }`: ECS
    counts failed task launches in a deployment. Past a threshold (for small
    services, 3 failed tasks; ECS uses half the desired count, minimum 3) it
    marks the deployment FAILED and redeploys the last revision that reached
    steady state.
  - `alarms { alarm_names = [target-5xx], rollback = true }`: ECS watches that
    alarm while the deployment is in progress and rolls back if it fires.
  - `health_check_grace_period_seconds` (90 for go-ticketing): ALB failures
    are ignored for this long after a task starts, to cover migrations.
  - `wait_for_steady_state = false`: Terraform returns as soon as the service
    update is accepted. Waiting is done by `verify-deploy.sh` instead, which
    is stricter (see §6).
  - `depends_on` the HTTP listener (a target group must be attached to a load
    balancer before a service can register targets) and the execution policy
    (so the first tasks don't start before they can read their secrets).
- **5xx alarm**: `HTTPCode_Target_5XX_Count`, sum per minute, >10 for 2
  consecutive minutes, missing data treated as "not breaching" (no traffic is
  healthy).

### outputs.tf

`url`, `ecr_repository_url`, `cluster_name`, `service_name`,
`task_definition_arn` (the exact revision this apply registered;
`verify-deploy.sh` waits on it), `task_role_name`,
`service_security_group_id`, `secret_arns` and a few others.

---

## 4. The per-app config

`infra/envs/go-ticketing/main.tf` is a single module call. Everything in it
that is specific to go-ticketing:

| Setting | Value | Reason (from the app's code) |
|---|---|---|
| `desired_count` | 1 | realtime fan-out is in-process (ADR 0004) |
| `health_check_path` | `/healthz` | liveness; see §3 alb.tf |
| grace period | 90s | migrations run at startup |
| `stop_timeout` | 30 | `server.Shutdown` waits up to 20s |
| `extra_egress_ports` | `[587]` | SMTP |
| `ENV=production` | — | turns on the API's strict config checks |
| `CORS_ORIGINS` | https origin | production rejects `http://` non-localhost origins |
| `SMTP_HOST` | required | production refuses to start without it |
| `DB_MAX_CONNS=10` | — | 2 tasks during a deploy × 10 is well under the t4g.micro's ~80 |
| `generated_secrets` | `JWT_SECRET` | |
| `external_secrets` | `SMTP_PASSWORD` | |
| `db_name`/`db_username` | `ticketing` | matches local dev |

`providers.tf` sets the region and `default_tags` (app, environment,
managed-by, repo) on every resource. `versions.tf` has an empty
`backend "s3" {}`: bucket, key and region are supplied at `init` time, so no
account details are committed.

Variables with no default are the deliberate decisions: `aws_region`,
`availability_zones`, `image_tag`, `cors_origins`, `public_base_url`,
`smtp_host/user/from`. `image_tag` never goes in a tfvars file. CI passes the
commit SHA with `-var`.

---

## 5. Secrets

| Env var | Source | Who can change it | In Terraform state? |
|---|---|---|---|
| `DATABASE_URL` | built from RDS + random password | Terraform | yes |
| `JWT_SECRET` | `random_password` | Terraform | yes |
| `SMTP_PASSWORD` | you, via CLI/console | you | only the placeholder |
| all other env vars | plain values in `main.tf`/tfvars | code review | yes (not secret) |

How a secret reaches the app: ECS starts a task → the agent assumes the
execution role → calls `GetSecretValue` for each ARN in the task definition →
injects the value as an environment variable → the Go `config.Load()` reads it.
Secret values never appear in the task definition, the image, or CI logs.

Things to know:

- **Generated values are in Terraform state.** The state bucket must be
  encrypted, versioned and private (§11). Terraform 1.11+ has write-only
  arguments that avoid this; the module targets 1.10 and doesn't use them.
- **Setting the SMTP password:**
  ```bash
  aws secretsmanager put-secret-value \
    --secret-id go-ticketing-sandbox/SMTP_PASSWORD --secret-string '...'
  aws ecs update-service --cluster go-ticketing-sandbox \
    --service go-ticketing-sandbox --force-new-deployment
  ```
  Secrets are read at task start, so running tasks keep the old value until
  they are replaced.
- **`sslmode=require`** encrypts the connection but doesn't verify the
  server's certificate. `verify-full` would need the RDS CA bundle baked into
  the image and `sslrootcert` in the URL. That's a reasonable next hardening
  step.

---

## 6. The deploy pipeline

The existing jobs are unchanged. The workflow gains a `workflow_dispatch`
trigger and four jobs:

```
push / PR:        backend ─┐
                  frontend ┴─▶ images
                  changes ──▶ infra            (only if infra/ or ci.yml changed)

manual run with deploy=true on main, and vars.DEPLOY_ENABLED == 'true':
                  backend ─┐
                  frontend ┴─▶ images ──▶ deploy-plan ──▶ deploy-apply
                              (saves image)              (environment: sandbox
                                                          → required reviewers)
```

### Concurrency (a change to the existing block)

The group now includes `github.event_name`, and `cancel-in-progress` is false
for manual runs. Before this change, a push to `main` would have cancelled an
in-flight manual run in the same group. Halfway through `terraform apply`,
that leaves the state lock held and the infrastructure half-changed.
`deploy-apply` also has its own `deploy-sandbox` concurrency group that queues
rather than cancels.

### `images` (extended)

On a deploy run, after building, it saves `ticketing-api` with `docker save`
and uploads it as an artifact (kept 1 day). The deploy job loads that exact
image. **What ships is the image that passed the tests**, not a rebuild.

### `changes` + `infra`

`dorny/paths-filter` decides whether `infra/**` or `ci.yml` changed. If so,
`infra`:

1. installs Terraform 1.17.2 (with the wrapper off, because it mangles stdout
   piped to `jq`),
2. shellchecks every script,
3. runs `make infra-plan` (§10),
4. runs `make infra-test` (§10).

No AWS credentials are involved.

### `deploy-plan`

1. **Gate**: manual run, `deploy` ticked, on `main`, `DEPLOY_ENABLED == 'true'`.
2. `IMAGE_TAG` is the commit SHA, or `inputs.image_tag` when re-pinning after
   a rollback.
3. Gets AWS credentials via **GitHub OIDC**: GitHub issues a short-lived token
   that AWS STS exchanges for the deploy role's temporary credentials. There
   are no AWS keys stored in GitHub.
4. Logs into ECR, downloads the image artifact, and pushes it as
   `<registry>/go-ticketing-sandbox:<sha>`. If the tag already exists (a
   re-run, or a re-pin), it skips the push: tags are immutable, so it's the
   same image by definition. If a re-pin asks for a tag that *doesn't* exist,
   it fails.
5. `terraform init` with backend settings from repository variables.
6. `terraform plan -var image_tag=$IMAGE_TAG -out=tfplan`, then
   `terraform show -no-color tfplan` (sensitive values print as
   `(sensitive value)`) is written to the **job summary** for the reviewer,
   and its SHA-256 becomes a job output.

### `deploy-apply`

1. Uses `environment: sandbox`. If that environment has required reviewers,
   the job **pauses until someone approves**. This is the manual gate.
2. Re-runs init and plan, and **compares the digest with the reviewed plan**.
   If anything changed in between (someone else applied, drift), it stops
   without applying.
   - Why re-plan instead of passing `tfplan` along: a saved plan file contains
     secret values in plain text, and workflow artifacts can be downloaded by
     anyone with read access to the repo.
3. `terraform apply tfplan`. This registers revision N+1 and points the service
   at it.
4. `verify-deploy.sh --task-definition <that exact ARN> --url <alb url>`. It
   polls `describe-services` and succeeds only when **that** deployment is
   PRIMARY, `rolloutState = COMPLETED` and running = desired, then curls
   `/healthz` and `/readyz` through the ALB. It fails if the deployment goes
   FAILED, stops being primary, disappears, or takes over 15 minutes, and
   prints the last service events.
   - Why not `aws ecs wait services-stable`: it gives up at 10 minutes, and it
     reports success when the service is stable on *any* revision, including
     the old one the circuit breaker just restored. A failed deploy would look
     green.

A marked comment (`To keep plans running but stop applies, comment out from
here`) shows exactly which steps to remove if you want plans without applies.

---

## 7. Anatomy of a deploy

A good deploy of revision 8 over revision 7, with the settings above (timings
are typical, not guaranteed):

| t | What happens |
|---|---|
| 0s | `terraform apply` registers `go-ticketing-sandbox:8`, updates the service. Deployment for :8 is PRIMARY, IN_PROGRESS; :7 is ACTIVE, still serving 100% of traffic. |
| ~5–40s | Fargate provisions a task: network interface, image pull from ECR, secrets fetched. |
| ~40s | Container starts: config validated → DB connect → migrations (advisory lock) → listens on :8080. |
| ~45s | Task registers in the target group. ALB probes `/healthz` every 10s. |
| ~65s | 2 passes → healthy. Traffic now goes to both :7 and :8. |
| ~65s | ECS deregisters the :7 task; the ALB stops sending it *new* requests. |
| ~95s | `deregistration_delay` (30s) ends; ECS sends SIGTERM to :7. Its graceful shutdown finishes in-flight requests (≤20s). Open WebSockets close; the browser reconnects to :8. |
| ~2 min | :8 is the only task. `rolloutState = COMPLETED`. `verify-deploy.sh` sees it, then `/healthz` and `/readyz` return 200. Job green. |

During the overlap two tasks run, which means realtime is briefly split
between them (ADR 0004). That's accepted for the length of a deploy.

---

## 8. When a deploy goes bad

| What's wrong with revision 8 | What happens on its own | What you see | What you do |
|---|---|---|---|
| Crashes on start (bad config, migration error, panic) | Task exits → ECS starts another → after ~3 failures the **circuit breaker** marks it FAILED and redeploys :7. The :7 task was never stopped. | `verify-deploy` fails: "rollout FAILED", plus service events. ~3–6 min. | Read logs in `/ecs/go-ticketing-sandbox`. Fix forward. Nothing to roll back. |
| Image can't be pulled / secret missing | Same: tasks fail with `ResourceInitializationError` → circuit breaker. | Same. | Check the IAM policy / secret exists. |
| Starts but `/healthz` fails | Each task gets 90s grace + ~30s of failures, then is replaced → circuit breaker after ~3. | Same, slower (~6–8 min). | Same. |
| Healthy but returns 5xx under traffic | If >10 5xx/min for 2 min **during the rollout**, the alarm fires → ECS rolls back. | `verify-deploy` fails ("no longer the primary" / "FAILED"). | Fix forward. |
| Healthy, low error rate, but wrong (logic bug), found later | Nothing. ECS can't know. | Users or monitoring tell you. | **`make rollback`** (§9), then re-pin or fix forward. |
| Bad migration (dropped column old code needs) | Nothing, and **rolling back code won't fix it**. | Errors from whichever code expects the old schema. | Fix forward, or RDS point-in-time restore (data loss since that time). Prevent it with backward-compatible migrations. |
| Bad infra change (SG rule, ALB setting) | Nothing; it isn't a task revision. | Connectivity errors. | `git revert` the change and deploy again. |
| Deploy job cancelled or runner died mid-apply | State lock may be held. | Next run fails to acquire the lock. | Check nobody is applying, then `terraform force-unlock <id>`. |

---

## 9. Rollback: the commands

Needs AWS credentials for the account (`AWS_PROFILE=...` or env vars). No
Terraform, no state access, no particular checkout.

```bash
# What can I roll back to? (marks the running revision with ->)
make revisions
#   -> 8      1234.dkr.ecr.eu-west-1.amazonaws.com/go-ticketing-sandbox:9f3c2e1...
#      7      1234.dkr.ecr.eu-west-1.amazonaws.com/go-ticketing-sandbox:41ab0d7...

# Back one revision (asks for confirmation)
make rollback

# To a specific revision, also probing the ALB afterwards
make rollback REVISION=6 URL=http://go-ticketing-sandbox-alb-123.eu-west-1.elb.amazonaws.com

# Directly, non-interactive
infra/scripts/rollback.sh --name go-ticketing-sandbox --to-revision 6 --yes
```

Step by step, `rollback.sh`:

1. `describe-services` → the current revision (say 8). Refuses if any
   deployment is IN_PROGRESS (the circuit breaker may already be rolling back),
   unless you pass `--force`.
2. `list-task-definitions --status ACTIVE --sort DESC`, filtered to *exactly*
   the family. `--family-prefix go-ticketing-sandbox` would also match
   `go-ticketing-sandbox-worker`; a regex removes look-alikes.
3. Target = `--to-revision`, or the newest ACTIVE revision below the current
   one.
4. Reads the target's image and runs `ecr describe-images` for that tag. If
   the lifecycle policy has expired it, the script refuses (that revision
   could never start).
5. Shows current vs target images and asks for confirmation (`--yes` skips).
6. `update-service --task-definition <target ARN>`. This is a normal rolling
   deployment with the same health gate and circuit breaker, so a rollback
   can't cause an outage either.
7. Runs `verify-deploy.sh` on the target revision.
8. Prints the **reconcile** note: Terraform state still names revision 8, so
   the next `terraform plan` will show the service moving back to 8. Before
   the next deploy, either:
   - **fix forward**: merge a fix and deploy normally, or
   - **re-pin**: Actions → CI → Run workflow on `main`, tick `deploy`, set
     `image_tag` to the good SHA the script printed. Terraform registers a
     *new* revision (9) with the good image, and state matches reality again.

---

## 10. Verifying without AWS

```bash
make infra-check            # = infra-plan + infra-test
make infra-plan TERRAFORM=/path/to/terraform-1.10+   # if your default is older
```

### `plan-offline.sh` (what `make infra-plan` runs)

1. `terraform fmt -check -recursive infra`.
2. Writes a temporary `zz_offline_override.tf` into the env directory.
   Terraform **merges `*_override.tf` files over** the real config. This one:
   - replaces `backend "s3" {}` with a local backend,
   - adds `skip_credentials_validation`, `skip_requesting_account_id`,
     `skip_metadata_api_check` and `skip_region_validation` to the provider,
     so it never calls STS or the EC2 metadata service.

   The file is deleted on exit (`trap`) and gitignored. The committed `.tf`
   files contain no test plumbing.
3. Sets fake `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY` (the provider wants
   *some* credentials to exist; they're never validated) and unsets
   `AWS_PROFILE`.
4. Uses `TF_DATA_DIR=.terraform-offline`, so it never interferes with a real
   `terraform init` in the same directory.
5. `init` → `validate` → `plan -refresh=false -var-file=example.tfvars
   -var image_tag=offline-plan`. Against empty state the plan is all creates
   (46 resources), and creates are computed locally.
6. `check-plan.sh` reads `terraform show -json` and asserts the invariants
   the rollback story depends on:
   - circuit breaker on with rollback,
   - minimum healthy ≥ 100, maximum > 100,
   - alarm rollback wired,
   - `skip_destroy`,
   - `image_tag` ≠ latest,
   - immutable ECR,
   - a health-check path,
   - DB not public, encrypted, final snapshot on.

   Each is one line a refactor could delete without anything else failing.
7. **Negative tests**: plans that must be *rejected*, `image_tag=latest` and a
   single AZ. Each checks the error message too, so a plan that fails for an
   unrelated reason doesn't count as a pass.

What this proves: syntax, formatting, types, variable wiring, validations, the
full resource graph, and the safety invariants. What it can't prove: that AWS
accepts every value (a bad instance class or an unavailable engine version
only fails at apply).

### `test/run.sh` (what `make infra-test` runs)

`fake-aws` is put first on `PATH` as `aws`. It logs every call and answers
from per-test JSON fixtures. `describe-services.1.json`, `.2.json`, ... are
returned in sequence (the last one repeats), which models a deployment
progressing over time. Each test builds its scenario, runs the real script,
and asserts on the exit code, the output, and which AWS calls were or weren't
made (for example, "update-service was never called").

The 16 tests cover:

- default/explicit target selection,
- look-alike families,
- an expired image,
- an in-progress deployment, and `--force`,
- a rollback whose own deployment fails,
- no older revision,
- an inactive revision,
- declining the prompt,
- `--list`,
- verify succeeding,
- verify detecting a circuit-breaker rollback,
- a deployment that vanished,
- "stable on the old revision",
- a timeout.

Two lessons from building it, both fixed and worth knowing:

- **Mutation testing found a dead test.** Removing the exact-family filter
  from `rollback.sh` made no test fail: the look-alike fixtures had *higher*
  revision numbers, which the "older than current" check skipped anyway. Now
  the look-alikes are listed first with a revision that would otherwise be
  chosen. All seven guards were then disabled one at a time, and each made at
  least one test fail.
- **Bash ignores `set -e` inside a function called from an `if`.** The tests
  run as `if ( test_fn )`, so a failed assertion that merely `return`ed was
  ignored and only each test's last line counted. Assertions now `exit 1`.
- `DEPLOY_TIMEOUT=3` in the tests means a regression that stops a terminal
  state being recognised fails in seconds instead of hanging for 15 minutes.

---

## 11. One-time AWS setup

You chose to create these by hand rather than with a bootstrap stack. Do this
once per AWS account, with admin credentials.

**a. State bucket.** Choose a globally unique name.

```bash
aws s3api create-bucket --bucket <state-bucket> --region <region> \
  --create-bucket-configuration LocationConstraint=<region>   # omit for us-east-1
aws s3api put-bucket-versioning --bucket <state-bucket> \
  --versioning-configuration Status=Enabled
aws s3api put-bucket-encryption --bucket <state-bucket> \
  --server-side-encryption-configuration '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"}}]}'
aws s3api put-public-access-block --bucket <state-bucket> \
  --public-access-block-configuration BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true
```

Versioning means a corrupted state can be recovered. Encryption and blocking
public access matter because the state holds the generated passwords.

**b. GitHub OIDC provider** (once per account):

```bash
aws iam create-open-id-connect-provider \
  --url https://token.actions.githubusercontent.com \
  --client-id-list sts.amazonaws.com
```

**c. Deploy role** that GitHub can assume. Trust policy (replace
`<account-id>`):

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": { "Federated": "arn:aws:iam::<account-id>:oidc-provider/token.actions.githubusercontent.com" },
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": { "token.actions.githubusercontent.com:aud": "sts.amazonaws.com" },
      "StringLike": {
        "token.actions.githubusercontent.com:sub": [
          "repo:njogubless/Go-Ticketing:ref:refs/heads/main",
          "repo:njogubless/Go-Ticketing:environment:sandbox"
        ]
      }
    }
  }]
}
```

The first `sub` is the `deploy-plan` job (no environment, on main). The second
is `deploy-apply` (environment `sandbox`). No other repo, branch or
environment can assume the role.

Permissions: Terraform needs to manage EC2/VPC, ELB, ECS, ECR, RDS, Secrets
Manager, CloudWatch, Logs, IAM (for the two roles), and the state bucket.
**This is a guess at a starting point, not a final answer:** attach
`PowerUserAccess`, plus an inline policy allowing `iam:*Role*`, `iam:*RolePolicy*`
and `iam:PassRole` on `arn:aws:iam::<account-id>:role/go-ticketing-*`. Tighten
it once a few real applies show which actions are actually used (IAM Access
Analyzer can generate a policy from CloudTrail).

---

## 12. Enabling deploys

1. Copy `infra/envs/go-ticketing/example.tfvars` to `sandbox.tfvars`, replace
   every `PLACEHOLDER` (region, AZs, origins, SMTP), and commit it. Nothing in
   it is secret.
2. GitHub → Settings → Environments → New environment `sandbox` → **Required
   reviewers**: yourself. Optionally restrict it to the `main` branch.
3. GitHub → Settings → Secrets and variables → Actions → **Variables** (not
   secrets; none of these are sensitive):

   | Variable | Value |
   |---|---|
   | `AWS_DEPLOY_ROLE_ARN` | the role from §11c |
   | `AWS_REGION` | your region |
   | `TF_STATE_BUCKET` | the bucket from §11a |
   | `ENVIRONMENT` | `sandbox` (optional; that's the default) |
   | `DEPLOY_ENABLED` | `true`, **last**, once the first apply (§13) has worked |

4. Deploy: Actions → CI → Run workflow → branch `main` → tick `deploy`.
   Review the plan in the `Deploy · plan` summary, then approve
   `Deploy · apply`.

To switch deploys off again, delete `DEPLOY_ENABLED` or set it to anything but
`true`.

---

## 13. The first apply

The first apply has a chicken-and-egg problem: the ECS service needs an image
in ECR, and ECR is created by the same apply. Do it by hand, once:

```bash
cd infra/envs/go-ticketing
cp backend.hcl.example backend.hcl        # fill in bucket + region
terraform init -backend-config=backend.hcl

# 1. Create only the repository
terraform apply -var-file=sandbox.tfvars -var image_tag=bootstrap \
  -target=module.api.aws_ecr_repository.this

# 2. Push the current image to it
SHA=$(git rev-parse HEAD)
REPO=$(terraform output -raw ecr_repository_url 2>/dev/null || \
  aws ecr describe-repositories --repository-names go-ticketing-sandbox \
    --query 'repositories[0].repositoryUri' --output text)
aws ecr get-login-password | docker login --username AWS --password-stdin "${REPO%%/*}"
docker build -t "$REPO:$SHA" ../../../backend && docker push "$REPO:$SHA"

# 3. Everything else (creates the secrets, with SMTP_PASSWORD = REPLACE_ME)
terraform apply -var-file=sandbox.tfvars -var image_tag="$SHA"

# 4. Set the real SMTP password and restart the task so it reads it
#    (until then the API runs, but email fails)
aws secretsmanager put-secret-value \
  --secret-id go-ticketing-sandbox/SMTP_PASSWORD --secret-string '...'
aws ecs update-service --cluster go-ticketing-sandbox \
  --service go-ticketing-sandbox --force-new-deployment

# 5. Verify
../../scripts/verify-deploy.sh --cluster go-ticketing-sandbox --service go-ticketing-sandbox \
  --task-definition "$(terraform output -raw task_definition_arn)" --url "$(terraform output -raw url)"
```

RDS takes ~5–10 minutes to create. Expect the full apply to take about 15
minutes. The first deploy has no previous revision, so if it fails there is
nothing to roll back to. Read the logs and fix it.

---

## 14. Rollback drill

Run once real AWS exists, so ADR 0010 can drop "not yet exercised". Each drill
has an expected result. If reality differs, the scripts or the ADR need
updating.

**Drill 1: the circuit breaker (automatic).** Push an image that exits
immediately, under a throwaway tag, and deploy it as a re-pin:

```bash
printf 'FROM alpine:3.20\nCMD ["sh","-c","echo drill; exit 1"]\n' | docker build -t "$REPO:drill-crash" -
docker push "$REPO:drill-crash"
# Actions → Run workflow → deploy ✓, image_tag = drill-crash
```

Expected:

- `verify-deploy` fails with "rollout FAILED" after ~3–6 minutes.
- `make revisions` shows the old revision running again.
- `curl <url>/healthz` returned 200 throughout.

Then re-pin to the good SHA.

**Drill 2: manual rollback.** Deploy two good commits (A, then B). Run
`make rollback`. Expected:

- the confirmation shows B → A,
- `verify-deploy` passes,
- the reconcile note prints A's SHA,
- `terraform plan` now shows the service changing to B's revision (the
  documented drift).

Re-pin with `image_tag=A`, and `terraform plan` is clean.

**Drill 3: the guards.**

- `make rollback REVISION=1` after more than 30 deploys should refuse
  (image expired).
- Running `make rollback` while a deploy is in progress should refuse.

---

## 15. Adding a second app

1. `cp -r infra/envs/go-ticketing infra/envs/<app>`, then delete
   `.terraform*` and any tfvars.
2. In `main.tf`, change `name`, `container_port`, `cpu`/`memory`,
   `health_check_path`, `environment`, `generated_secrets` (e.g. Django's
   `SECRET_KEY`), `external_secrets`, `db_name`/`db_username`, and
   `extra_egress_ports`.
3. Trim `variables.tf` to that app's inputs, and update `providers.tf`'s `app`
   tag.
4. Add a deploy job pair for it (or generalise the existing one with a matrix,
   once there are two to compare).

What will probably need module changes, and was deliberately **not** built
yet:

- a startup command override (Django `migrate` as a separate one-off task,
  instead of migrating in-process like the Go app),
- a container health-check command,
- a shared ALB with host-based routing,
- worker services with no load balancer.

Build these when the second app shows the actual shape.

---

## 16. Assumptions, guesses and known gaps

**Decided by you:**

- lean sandbox network,
- backend only (no frontend in AWS yet),
- no domain (HTTP until `certificate_arn` is set),
- rolling + circuit breaker over blue/green,
- no region chosen,
- no bootstrap stack,
- nothing applied.

**Guesses, flagged:**

- `example.tfvars` uses `eu-west-1` purely as a placeholder so the plan has a
  syntactically valid region.
- Environment name `sandbox`, and the naming scheme `<app>-<environment>`.
- The cost estimate in ADR 0009 (~$45–55/month) is from list prices, not a
  bill.
- The deploy role's permissions (§11c) are a starting point.
- AWS provider 6.x and Terraform 1.17.2 in CI (the code requires ≥1.10). Your local Terraform was 1.2.3,
  which is too old: install ≥1.10 or pass `TERRAFORM=` to make.
- That ECS's real `describe-services` responses match the fixtures in
  `test/run.sh` (they follow the documented shape; §14 confirms it).

**Known gaps:**

- The frontend isn't deployed. `web/nginx.conf` hard-codes
  `proxy_pass http://api:8080`, which won't resolve on ECS. It needs changing
  before the web image can run there.
- Migrations aren't reversible. Rollback requires backward-compatible
  migrations (ADR 0010).
- `sslmode=require`, not `verify-full`.
- Generated secrets are in Terraform state.
- Tasks have public IPs (locked down by security group, but present).
- Single-AZ RDS; no WAF; no VPC flow logs; no autoscaling (by design, ADR 0004).
- No alerting to a human. The 5xx alarm drives rollbacks but notifies nobody.
  Adding an SNS topic → email is a small next step.
- `.terraform.lock.hcl` was generated on linux/amd64. On macOS, run
  `terraform providers lock -platform=darwin_arm64 -platform=linux_amd64` if
  init complains.

---

## 17. The two-minute explanation

> The app is a Go API with Postgres. It runs on ECS Fargate behind an ALB, all
> in Terraform: a generic `containerized-service` module, plus a fifteen-line
> per-app config, so my other services reuse the same module.
>
> Every deploy is the commit SHA as an immutable ECR tag, which becomes a new
> numbered task-definition revision. Deploys are rolling with 100% minimum
> healthy, so the old task keeps serving until the new one passes the ALB
> health check. It fails closed.
>
> If the new revision crashes or fails health checks, ECS's circuit breaker
> rolls back automatically. If it's healthy but throwing 5xxs during the
> rollout, a CloudWatch alarm attached to the deployment does the same. For a
> bug found later, `make rollback` points the service at the previous revision.
> It needs only AWS credentials, checks the old image still exists, refuses if
> a deploy is already in flight, and waits for that specific revision to
> complete. Then I re-pin Terraform with the good SHA so state matches reality.
>
> What rollback doesn't cover is the database: migrations only go forward, so
> they have to be backward-compatible with the previous release.
>
> All of it is checked in CI without AWS credentials: `terraform plan` against
> fake credentials, assertions on the plan's safety settings, and a test suite
> for the rollback script against a fake AWS CLI, which I mutation-tested.
>
> The deploy itself is manual, approval-gated, and refuses to apply if the plan
> changed after review.
