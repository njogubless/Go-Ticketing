# The module declares the providers it needs but never configures them.
# Region, credentials and default tags belong to the caller (envs/<app>), so the
# same module can be instantiated in any account or region without editing it.

terraform {
  # 1.10: S3 backend native locking (use_lockfile) in the callers, and
  # cross-variable references inside `validation` blocks (1.9+), which this
  # module relies on to reject inconsistent network settings at plan time.
  required_version = ">= 1.10"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = ">= 6.0, < 7.0"
    }
    random = {
      source  = "hashicorp/random"
      version = ">= 3.6, < 4.0"
    }
  }
}
