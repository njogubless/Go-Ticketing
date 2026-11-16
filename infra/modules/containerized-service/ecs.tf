# ECS on Fargate: cluster, task definition, service.
#
# Deploy model: every distinct image_tag (or any other container setting)
# produces a new, numbered task-definition revision. The service is pointed at
# the new revision and performs a rolling deployment gated on the ALB health
# check. Old revisions stay registered (skip_destroy) so any of them can be
# redeployed by number — that is the rollback mechanism. See
# docs/adr/0010-rollback-mechanism.md.

resource "aws_cloudwatch_log_group" "this" {
  name              = "/ecs/${var.name}"
  retention_in_days = var.log_retention_days
  tags              = var.tags
}

resource "aws_ecs_cluster" "this" {
  name = var.name

  # Container Insights adds per-task CPU/memory metrics at extra CloudWatch
  # cost. Left off for a single-task service; the ALB metrics cover the
  # deployment-safety alarms.
  setting {
    name  = "containerInsights"
    value = "disabled"
  }

  tags = var.tags
}

locals {
  image = "${aws_ecr_repository.this.repository_url}:${var.image_tag}"

  container_definition = {
    name      = var.container_name
    image     = local.image
    essential = true

    portMappings = [{
      containerPort = var.container_port
      protocol      = "tcp"
    }]

    # Sorted so that reordering the map in tfvars never produces a spurious diff
    # (and therefore a spurious new revision).
    environment = [
      for k in sort(keys(var.environment)) : { name = k, value = var.environment[k] }
    ]
    secrets = [
      for k in sort(keys(local.secret_arns)) : { name = k, valueFrom = local.secret_arns[k] }
    ]

    readonlyRootFilesystem = var.readonly_root_filesystem
    stopTimeout            = var.stop_timeout

    logConfiguration = {
      logDriver = "awslogs"
      options = {
        awslogs-group         = aws_cloudwatch_log_group.this.name
        awslogs-region        = data.aws_region.current.region
        awslogs-stream-prefix = var.container_name
      }
    }
  }
}

# Resolved locally from the provider configuration — no API call, so it is safe
# for credential-less plans.
data "aws_region" "current" {}

resource "aws_ecs_task_definition" "this" {
  family                   = var.name
  requires_compatibilities = ["FARGATE"]
  network_mode             = "awsvpc"
  cpu                      = var.cpu
  memory                   = var.memory
  execution_role_arn       = aws_iam_role.execution.arn
  task_role_arn            = aws_iam_role.task.arn

  container_definitions = jsonencode([local.container_definition])

  runtime_platform {
    operating_system_family = "LINUX"
    cpu_architecture        = var.cpu_architecture
  }

  # Without this, every new revision makes Terraform deregister the previous
  # one, and the rollback target disappears at exactly the moment it is needed.
  # Revisions are free; keeping them is the whole rollback mechanism.
  skip_destroy = true

  tags = var.tags

  lifecycle {
    precondition {
      condition     = length(setintersection(keys(var.environment), keys(local.secret_arns))) == 0
      error_message = "A variable cannot be both in `environment` and a secret: ${join(", ", setintersection(keys(var.environment), keys(local.secret_arns)))}."
    }
    precondition {
      condition     = length(var.generated_secrets) + length(var.external_secrets) + 1 == length(keys(local.secret_arns))
      error_message = "generated_secrets and external_secrets must be distinct from each other and must not include DATABASE_URL."
    }
  }
}

resource "aws_ecs_service" "this" {
  name            = var.name
  cluster         = aws_ecs_cluster.this.id
  task_definition = aws_ecs_task_definition.this.arn
  desired_count   = var.desired_count
  launch_type     = "FARGATE"

  # --- fail-closed rolling deployment ----------------------------------------
  #
  # 100% minimum healthy: no old task is stopped until a new one is healthy.
  # 200% maximum: a full set of new tasks can run beside the old ones.
  # With desired_count = 1 this means: start new → pass ALB health → drain old.
  deployment_minimum_healthy_percent = var.deployment_minimum_healthy_percent
  deployment_maximum_percent         = var.deployment_maximum_percent

  # Circuit breaker: if new tasks keep failing to start or keep failing health
  # checks, ECS marks the deployment FAILED and redeploys the last revision that
  # reached steady state. The old tasks were never stopped (see above), so users
  # see no outage.
  deployment_circuit_breaker {
    enable   = true
    rollback = true
  }

  # Alarm-based rollback: catches a revision that is healthy (passes /healthz)
  # but serving errors. ECS watches these alarms during the deployment and rolls
  # back if one fires.
  dynamic "alarms" {
    for_each = var.rollback_on_5xx ? [1] : []
    content {
      alarm_names = [aws_cloudwatch_metric_alarm.target_5xx[0].alarm_name]
      enable      = true
      rollback    = true
    }
  }

  health_check_grace_period_seconds = var.health_check_grace_period_seconds

  load_balancer {
    target_group_arn = aws_lb_target_group.this.arn
    container_name   = var.container_name
    container_port   = var.container_port
  }

  network_configuration {
    subnets          = local.task_subnet_ids
    security_groups  = [aws_security_group.service.id]
    assign_public_ip = !var.tasks_in_private_subnets
  }

  propagate_tags = "SERVICE"

  # Terraform does not wait for the rollout here. The deploy pipeline runs
  # scripts/verify-deploy.sh instead, which tracks *this* revision's deployment
  # to COMPLETED or FAILED and then probes /readyz — a stricter and more
  # explicit gate than the provider's steady-state waiter.
  wait_for_steady_state = false

  tags = var.tags

  # The target group must be attached to a listener before a service can
  # register targets in it.
  depends_on = [aws_lb_listener.http, aws_iam_role_policy.execution]
}

# --- alarm used by the deployment ---------------------------------------------

resource "aws_cloudwatch_metric_alarm" "target_5xx" {
  count = var.rollback_on_5xx ? 1 : 0

  alarm_name        = "${var.name}-target-5xx"
  alarm_description = "Target 5xx responses on ${var.name}. Wired into the ECS service's deployment configuration: firing during a deployment rolls it back."

  namespace   = "AWS/ApplicationELB"
  metric_name = "HTTPCode_Target_5XX_Count"
  dimensions = {
    LoadBalancer = aws_lb.this.arn_suffix
    TargetGroup  = aws_lb_target_group.this.arn_suffix
  }

  statistic           = "Sum"
  period              = 60
  evaluation_periods  = 2
  threshold           = var.rollback_5xx_threshold
  comparison_operator = "GreaterThanThreshold"

  # No traffic means no data points, which is healthy, not alarming.
  treat_missing_data = "notBreaching"

  tags = var.tags
}
