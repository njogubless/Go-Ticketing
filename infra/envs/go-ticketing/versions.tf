terraform {
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 6.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
  }

  # Partial configuration: bucket, key and region come from
  # `terraform init -backend-config=backend.hcl` (see backend.hcl.example), so
  # no account-specific value is committed. use_lockfile = true in that file
  # gives S3-native state locking, with no DynamoDB table.
  backend "s3" {}
}
