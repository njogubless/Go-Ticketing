# Only what genuinely differs per environment/account. Everything else about
# go-ticketing is fixed in main.tf.

variable "aws_region" {
  description = "AWS region to deploy into. No default: choosing one is a deliberate decision."
  type        = string
}

variable "availability_zones" {
  description = "Two or more AZs in aws_region, e.g. [\"eu-west-1a\", \"eu-west-1b\"]."
  type        = list(string)
}

variable "environment" {
  description = "Environment name; becomes part of every resource name (go-ticketing-<environment>)."
  type        = string
  default     = "sandbox"
}

variable "image_tag" {
  description = "Image tag to deploy (the git commit SHA). Supplied by CI with -var, never stored in tfvars."
  type        = string
}

variable "certificate_arn" {
  description = "ACM certificate ARN for HTTPS. null = HTTP only on the ALB's AWS hostname."
  type        = string
  default     = null
}

variable "cors_origins" {
  description = "Comma-separated browser origins allowed to call the API. ENV=production requires https:// origins."
  type        = string
}

variable "public_base_url" {
  description = "Base URL used in links inside notification emails."
  type        = string
}

variable "smtp_host" {
  description = "SMTP relay host. Required: the API refuses to start in production without one."
  type        = string
}

variable "smtp_port" {
  type    = number
  default = 587
}

variable "smtp_user" {
  type = string
}

variable "smtp_from" {
  type = string
}
