# EXAMPLE VALUES ONLY — used by `make infra-plan` and the CI plan job, which run
# without AWS credentials. Nothing here is real.
#
# For a real deployment: copy to sandbox.tfvars, replace every PLACEHOLDER, and
# commit it (none of these are secrets — secrets live in Secrets Manager).

aws_region         = "eu-west-1"                  # PLACEHOLDER — region not chosen yet
availability_zones = ["eu-west-1a", "eu-west-1b"] # PLACEHOLDER — must match aws_region
environment        = "sandbox"

# No domain yet: HTTP on the ALB hostname. Set an ACM ARN to enable HTTPS.
certificate_arn = null

# The API (ENV=production) requires https origins and an SMTP host at startup.
cors_origins    = "https://app.example.com" # PLACEHOLDER
public_base_url = "https://app.example.com" # PLACEHOLDER
smtp_host       = "smtp.example.com"        # PLACEHOLDER
smtp_port       = 587
smtp_user       = "PLACEHOLDER"
smtp_from       = "servicedesk@example.com" # PLACEHOLDER

# image_tag is deliberately absent: CI passes -var image_tag=<git sha>.
