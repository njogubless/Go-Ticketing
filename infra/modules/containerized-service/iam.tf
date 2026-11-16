# Two roles, because ECS has two distinct principals:
#
#   execution role — used by the ECS agent *before* the app starts: pull the
#                    image, fetch secrets, create the log stream.
#   task role      — the identity the app's own code runs as.
#
# Neither uses the AWS-managed AmazonECSTaskExecutionRolePolicy: that grants
# ECR pull on every repository and logs on every group in the account. These
# are scoped to this service's repository, log group and secrets only.

data "aws_iam_policy_document" "ecs_tasks_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "execution" {
  name               = "${var.name}-execution"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
  tags               = var.tags
}

data "aws_iam_policy_document" "execution" {
  # GetAuthorizationToken has no resource-level scoping in IAM; "*" is the only
  # value AWS accepts. The token alone grants nothing — the pull actions below
  # are what is scoped.
  statement {
    sid       = "EcrAuth"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "EcrPullThisRepo"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:GetDownloadUrlForLayer",
    ]
    resources = [aws_ecr_repository.this.arn]
  }

  statement {
    sid       = "WriteOwnLogs"
    actions   = ["logs:CreateLogStream", "logs:PutLogEvents"]
    resources = ["${aws_cloudwatch_log_group.this.arn}:*"]
  }

  statement {
    sid       = "ReadOwnSecrets"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = values(local.secret_arns)
  }
}

resource "aws_iam_role_policy" "execution" {
  name   = "${var.name}-execution"
  role   = aws_iam_role.execution.id
  policy = data.aws_iam_policy_document.execution.json
}

# The task role starts with no permissions at all — go-ticketing calls no AWS
# APIs. A future app that needs, say, S3 attaches a policy to this role from its
# envs/<app> config (see the task_role_name output) without touching the module.
resource "aws_iam_role" "task" {
  name               = "${var.name}-task"
  assume_role_policy = data.aws_iam_policy_document.ecs_tasks_assume.json
  tags               = var.tags
}
