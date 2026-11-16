# Re-exported so CI and scripts read them with `terraform output -raw <name>`.

output "url" {
  value = module.api.url
}

output "ecr_repository_url" {
  value = module.api.ecr_repository_url
}

output "cluster_name" {
  value = module.api.cluster_name
}

output "service_name" {
  value = module.api.service_name
}

output "task_definition_arn" {
  value = module.api.task_definition_arn
}

output "task_definition_revision" {
  value = module.api.task_definition_revision
}

output "log_group_name" {
  value = module.api.log_group_name
}

output "secret_arns" {
  value = module.api.secret_arns
}
