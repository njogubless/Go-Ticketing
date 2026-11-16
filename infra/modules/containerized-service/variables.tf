# Inputs are grouped by concern. Every variable either has a safe default or is
# genuinely app-specific — if you find yourself setting the same value in every
# envs/<app>, it should become a default here instead.

# --- identity ----------------------------------------------------------------

variable "name" {
  description = "Base name for every resource, e.g. \"go-ticketing-sandbox\". The ECS cluster, service, task-definition family, ECR repository and log group are all named exactly this; scripts/rollback.sh relies on that."
  type        = string

  validation {
    # 24 leaves room for the longest suffix this module adds ("-alb", "-tg",
    # "-final") inside AWS's 32-character limit for ALB and target group names.
    condition     = can(regex("^[a-z][a-z0-9-]{1,22}[a-z0-9]$", var.name))
    error_message = "name must be 3-24 chars, lowercase letters, digits and hyphens, starting with a letter."
  }
}

variable "tags" {
  description = "Extra tags for resources that support them (on top of the provider's default_tags)."
  type        = map(string)
  default     = {}
}

# --- network -----------------------------------------------------------------

variable "create_vpc" {
  description = "true: create a dedicated VPC. false: use existing_vpc_id and the existing_* subnet IDs."
  type        = bool
  default     = true
}

variable "vpc_cidr" {
  description = "CIDR for the created VPC. Only used when create_vpc = true."
  type        = string
  default     = "10.40.0.0/16"
}

variable "availability_zones" {
  description = "AZs to spread subnets across, e.g. [\"eu-west-1a\", \"eu-west-1b\"]. Passed explicitly instead of looked up so `terraform plan` works without AWS credentials. RDS requires at least two."
  type        = list(string)
  default     = []

  validation {
    condition     = !var.create_vpc || length(var.availability_zones) >= 2
    error_message = "availability_zones needs at least two entries when create_vpc = true (RDS subnet groups and ALBs both require two AZs)."
  }
}

variable "existing_vpc_id" {
  description = "VPC to deploy into when create_vpc = false."
  type        = string
  default     = null

  validation {
    condition     = var.create_vpc || var.existing_vpc_id != null
    error_message = "existing_vpc_id is required when create_vpc = false."
  }
}

variable "existing_public_subnet_ids" {
  description = "Public subnets (route to an internet gateway) for the ALB, and for tasks when tasks_in_private_subnets = false. Used when create_vpc = false."
  type        = list(string)
  default     = []

  validation {
    condition     = var.create_vpc || length(var.existing_public_subnet_ids) >= 2
    error_message = "existing_public_subnet_ids needs at least two subnets when create_vpc = false."
  }
}

variable "existing_private_subnet_ids" {
  description = "Private subnets for RDS, and for tasks when tasks_in_private_subnets = true. Used when create_vpc = false."
  type        = list(string)
  default     = []

  validation {
    condition     = var.create_vpc || length(var.existing_private_subnet_ids) >= 2
    error_message = "existing_private_subnet_ids needs at least two subnets when create_vpc = false."
  }
}

variable "tasks_in_private_subnets" {
  description = "false (cheap): tasks run in public subnets with a public IP, reachable only from the ALB's security group. true: tasks run in private subnets and need a NAT gateway (or VPC endpoints) to reach ECR, Secrets Manager and CloudWatch."
  type        = bool
  default     = false
}

variable "create_nat_gateway" {
  description = "Create one NAT gateway (in the first public subnet) for the private subnets. About $32/month plus data. Only meaningful with create_vpc = true."
  type        = bool
  default     = false

  validation {
    # Without a route out, Fargate cannot pull the image or read secrets and
    # every task fails with an opaque ResourceInitializationError. Catch it here.
    condition     = !(var.create_vpc && var.tasks_in_private_subnets) || var.create_nat_gateway
    error_message = "tasks_in_private_subnets = true with create_vpc = true requires create_nat_gateway = true, or tasks cannot reach ECR."
  }
}

variable "alb_ingress_cidrs" {
  description = "CIDRs allowed to reach the load balancer."
  type        = list(string)
  default     = ["0.0.0.0/0"]
}

variable "extra_egress_ports" {
  description = "TCP ports the app may reach anywhere, beyond 443 (AWS APIs) and the database. e.g. [587] for SMTP."
  type        = list(number)
  default     = []
}

# --- TLS ---------------------------------------------------------------------

variable "certificate_arn" {
  description = "ACM certificate for an HTTPS listener. null: the ALB serves plain HTTP only. Set it and HTTP starts redirecting to HTTPS."
  type        = string
  default     = null
}

# --- container ---------------------------------------------------------------

variable "image_tag" {
  description = "Tag of the image in this module's ECR repository to run. CI passes the git commit SHA; each distinct value produces a new task-definition revision."
  type        = string

  validation {
    # "latest" would make a task-definition revision mean different bits at
    # different times, which defeats revision-based rollback entirely.
    condition     = var.image_tag != "latest" && can(regex("^[A-Za-z0-9_][A-Za-z0-9_.-]{0,127}$", var.image_tag))
    error_message = "image_tag must be a valid, immutable Docker tag (not \"latest\")."
  }
}

variable "container_name" {
  description = "Name of the container inside the task definition."
  type        = string
  default     = "app"
}

variable "container_port" {
  description = "Port the app listens on inside the container."
  type        = number
  default     = 8080
}

variable "cpu" {
  description = "Fargate task CPU units (256 = 0.25 vCPU)."
  type        = number
  default     = 256
}

variable "memory" {
  description = "Fargate task memory in MiB. Must be a valid pairing with cpu."
  type        = number
  default     = 512
}

variable "cpu_architecture" {
  description = "X86_64 or ARM64. Must match the architecture the image was built for."
  type        = string
  default     = "X86_64"

  validation {
    condition     = contains(["X86_64", "ARM64"], var.cpu_architecture)
    error_message = "cpu_architecture must be X86_64 or ARM64."
  }
}

variable "desired_count" {
  description = "Number of tasks to run in steady state."
  type        = number
  default     = 1
}

variable "environment" {
  description = "Plain (non-secret) environment variables for the container."
  type        = map(string)
  default     = {}
}

variable "generated_secrets" {
  description = "Secret env var names whose values Terraform generates (48 random alphanumerics) and stores in Secrets Manager, e.g. [\"JWT_SECRET\"]."
  type        = list(string)
  default     = []
}

variable "external_secrets" {
  description = "Secret env var names whose values you set yourself, out of band, e.g. [\"SMTP_PASSWORD\"]. Terraform creates each secret with the placeholder REPLACE_ME and never overwrites it afterwards."
  type        = list(string)
  default     = []
}

variable "readonly_root_filesystem" {
  description = "Mount the container's root filesystem read-only."
  type        = bool
  default     = true
}

variable "stop_timeout" {
  description = "Seconds between SIGTERM and SIGKILL. Must exceed the app's own graceful-shutdown timeout."
  type        = number
  default     = 30
}

variable "log_retention_days" {
  description = "CloudWatch log retention."
  type        = number
  default     = 30
}

# --- health & deployment -----------------------------------------------------

variable "health_check_path" {
  description = "Path the ALB probes. ECS replaces tasks that fail it, and a deployment only progresses once new tasks pass it."
  type        = string
  default     = "/healthz"
}

variable "health_check_grace_period_seconds" {
  description = "How long after a task starts ECS ignores ALB health failures. Covers startup work such as migrations."
  type        = number
  default     = 60
}

variable "deregistration_delay" {
  description = "Seconds the ALB keeps draining in-flight requests to a target before ECS stops it."
  type        = number
  default     = 30
}

variable "alb_idle_timeout" {
  description = "Seconds an idle connection (e.g. a WebSocket between pings) is kept open by the ALB."
  type        = number
  default     = 120
}

variable "deployment_minimum_healthy_percent" {
  description = "Lower bound on running, healthy tasks during a deployment, as a percentage of desired_count. 100 means an old task is never stopped before its replacement is healthy."
  type        = number
  default     = 100
}

variable "deployment_maximum_percent" {
  description = "Upper bound on tasks during a deployment. 200 lets a full new set start beside the old one."
  type        = number
  default     = 200
}

variable "rollback_on_5xx" {
  description = "Attach a CloudWatch alarm on target 5xx responses to the service's deployment configuration, so ECS also rolls back a deployment that is healthy but erroring."
  type        = bool
  default     = true
}

variable "rollback_5xx_threshold" {
  description = "Target 5xx responses per minute, sustained for two consecutive minutes, that trigger the rollback alarm."
  type        = number
  default     = 10
}

# --- database ----------------------------------------------------------------

variable "create_database" {
  description = "true: create a dedicated RDS PostgreSQL instance and a DATABASE_URL secret. false: pass existing_database_url_secret_arn instead (e.g. a shared instance)."
  type        = bool
  default     = true
}

variable "existing_database_url_secret_arn" {
  description = "Secrets Manager ARN whose value is a full DATABASE_URL. Required when create_database = false. Network access to that database is the caller's job; see the service_security_group_id output."
  type        = string
  default     = null

  validation {
    condition     = var.create_database || var.existing_database_url_secret_arn != null
    error_message = "existing_database_url_secret_arn is required when create_database = false."
  }
}

variable "db_engine_version" {
  description = "PostgreSQL major (or major.minor) version."
  type        = string
  default     = "16"
}

variable "db_instance_class" {
  description = "RDS instance class."
  type        = string
  default     = "db.t4g.micro"
}

variable "db_allocated_storage" {
  description = "Initial storage in GiB."
  type        = number
  default     = 20
}

variable "db_max_allocated_storage" {
  description = "Storage autoscaling ceiling in GiB."
  type        = number
  default     = 100
}

variable "db_name" {
  description = "Database created inside the instance."
  type        = string
  default     = "app"
}

variable "db_username" {
  description = "Master username."
  type        = string
  default     = "app"
}

variable "db_multi_az" {
  description = "Standby replica in a second AZ. Roughly doubles database cost."
  type        = bool
  default     = false
}

variable "db_backup_retention_days" {
  description = "Automated backup retention. Point-in-time recovery is the only undo for a destructive migration."
  type        = number
  default     = 7
}

variable "db_deletion_protection" {
  description = "Block deletion of the RDS instance (including via terraform destroy) until this is set to false."
  type        = bool
  default     = true
}

# --- registry & secrets housekeeping -----------------------------------------

variable "ecr_keep_images" {
  description = "How many tagged images ECR keeps. This is also how far back a rollback can reach, because a task definition whose image has been expired cannot start."
  type        = number
  default     = 30
}

variable "secret_recovery_window_days" {
  description = "Days a deleted secret can be restored. 0 deletes immediately (useful when tearing down sandboxes repeatedly)."
  type        = number
  default     = 7
}
