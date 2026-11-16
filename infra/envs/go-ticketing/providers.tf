provider "aws" {
  region = var.aws_region

  # Applied to every taggable resource the module creates, so cost reports and
  # the console can be filtered by app and environment without the module
  # having to know either.
  default_tags {
    tags = {
      app         = "go-ticketing"
      environment = var.environment
      managed-by  = "terraform"
      repo        = "Go-Ticketing"
    }
  }
}
