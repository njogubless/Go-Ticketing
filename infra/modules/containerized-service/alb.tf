# Application Load Balancer: the only public entry point.
#
# Its target group health check is what makes deployments fail closed — ECS
# will not stop an old task until a new one has passed it, and will not count a
# new task as part of the deployment until it does.

resource "aws_lb" "this" {
  name               = "${var.name}-alb"
  load_balancer_type = "application"
  internal           = false
  security_groups    = [aws_security_group.alb.id]
  subnets            = local.public_subnet_ids

  # WebSockets idle between pings. The app pings every 45s, so the 60s default
  # would work, but only just; this leaves headroom.
  idle_timeout = var.alb_idle_timeout

  # Rejects requests with malformed headers instead of forwarding them — a
  # request-smuggling defence that costs nothing.
  drop_invalid_header_fields = true

  tags = merge(var.tags, { Name = "${var.name}-alb" })
}

resource "aws_lb_target_group" "this" {
  name                 = "${var.name}-tg"
  port                 = var.container_port
  protocol             = "HTTP"
  target_type          = "ip" # required for Fargate (awsvpc networking)
  vpc_id               = local.vpc_id
  deregistration_delay = var.deregistration_delay

  health_check {
    path                = var.health_check_path
    matcher             = "200"
    interval            = 10
    timeout             = 5
    healthy_threshold   = 2 # ~20s for a new task to be trusted
    unhealthy_threshold = 3 # ~30s of failures before a task is pulled
  }

  tags = merge(var.tags, { Name = "${var.name}-tg" })
}

# Port 80 forwards when there is no certificate, and redirects to HTTPS when
# there is one. One resource with a dynamic action, so enabling TLS is a change
# in place, not a destroy-and-recreate of the listener.
resource "aws_lb_listener" "http" {
  load_balancer_arn = aws_lb.this.arn
  port              = 80
  protocol          = "HTTP"

  default_action {
    type             = var.certificate_arn == null ? "forward" : "redirect"
    target_group_arn = var.certificate_arn == null ? aws_lb_target_group.this.arn : null

    dynamic "redirect" {
      for_each = var.certificate_arn == null ? [] : [1]
      content {
        port        = "443"
        protocol    = "HTTPS"
        status_code = "HTTP_301"
      }
    }
  }
}

resource "aws_lb_listener" "https" {
  count = var.certificate_arn == null ? 0 : 1

  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = var.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.this.arn
  }
}
