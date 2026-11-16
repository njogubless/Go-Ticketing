# go-ticketing on the containerized-service module.
#
# This file is the whole per-app surface. Wiring up another backend means
# copying this directory and changing what is marked APP-SPECIFIC below.

module "api" {
  source = "../../modules/containerized-service"

  name               = "go-ticketing-${var.environment}"
  availability_zones = var.availability_zones
  image_tag          = var.image_tag
  certificate_arn    = var.certificate_arn

  # --- APP-SPECIFIC: container ----------------------------------------------
  container_port = 8080
  cpu            = 256
  memory         = 512

  # 1, not more: realtime fan-out is in-process (ADR 0004), so a second replica
  # would not see the first one's events. Deployments briefly run 2 (the
  # rolling overlap), which is acceptable for the length of a deploy.
  desired_count = 1

  # The API runs migrations before it starts listening, so /healthz answering
  # at all means the database was reachable and the schema is current. That is
  # why the ALB probes liveness rather than /readyz — see ADR 0009.
  health_check_path = "/healthz"

  # Migrations run at startup behind an advisory lock; give them room.
  health_check_grace_period_seconds = 90

  # The API's graceful shutdown waits up to 20s; SIGKILL must come later.
  stop_timeout = 30

  # SMTP for notification email.
  extra_egress_ports = [var.smtp_port]

  environment = {
    ENV             = "production"
    PORT            = "8080"
    LOG_FORMAT      = "json"
    LOG_LEVEL       = "info"
    CORS_ORIGINS    = var.cors_origins
    PUBLIC_BASE_URL = var.public_base_url
    SMTP_HOST       = var.smtp_host
    SMTP_PORT       = tostring(var.smtp_port)
    SMTP_USER       = var.smtp_user
    SMTP_FROM       = var.smtp_from
    # db.t4g.micro allows ~80 connections. 10 per task leaves room for the
    # rolling-deploy overlap (2 tasks) and a psql session with margin to spare.
    DB_MAX_CONNS = "10"
  }

  generated_secrets = ["JWT_SECRET"]
  external_secrets  = ["SMTP_PASSWORD"]

  # --- APP-SPECIFIC: database -----------------------------------------------
  db_name     = "ticketing"
  db_username = "ticketing"

  # --- cost posture: lean sandbox (the module defaults) ---------------------
  # tasks_in_private_subnets = false, create_nat_gateway = false,
  # db_instance_class = "db.t4g.micro", db_multi_az = false.
}
