output "url" {
  description = "Base URL of the service."
  value       = var.certificate_arn == null ? "http://${aws_lb.this.dns_name}" : "https://${aws_lb.this.dns_name}"
}

output "alb_dns_name" {
  description = "Point a CNAME/alias here once you have a domain."
  value       = aws_lb.this.dns_name
}

output "ecr_repository_url" {
  description = "Push images here, tagged with the commit SHA."
  value       = aws_ecr_repository.this.repository_url
}

output "cluster_name" {
  value = aws_ecs_cluster.this.name
}

output "service_name" {
  value = aws_ecs_service.this.name
}

output "task_definition_family" {
  value = aws_ecs_task_definition.this.family
}

output "task_definition_arn" {
  description = "The revision this apply registered. scripts/verify-deploy.sh waits on exactly this ARN."
  value       = aws_ecs_task_definition.this.arn
}

output "task_definition_revision" {
  value = aws_ecs_task_definition.this.revision
}

output "log_group_name" {
  value = aws_cloudwatch_log_group.this.name
}

output "service_security_group_id" {
  description = "Allow this group into a shared database when create_database = false."
  value       = aws_security_group.service.id
}

output "task_role_name" {
  description = "Attach app-specific AWS permissions (S3, SQS, ...) to this role from the caller."
  value       = aws_iam_role.task.name
}

output "database_endpoint" {
  value = var.create_database ? aws_db_instance.this[0].endpoint : null
}

output "secret_arns" {
  description = "Env var name => Secrets Manager ARN."
  value       = local.secret_arns
}

output "vpc_id" {
  value = local.vpc_id
}
