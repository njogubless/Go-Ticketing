# Container registry. One repository per service, named exactly var.name.

resource "aws_ecr_repository" "this" {
  name = var.name

  # IMMUTABLE: once a tag is pushed it can never point at different bits. This
  # is what makes "task-definition revision N" a stable thing to roll back to —
  # with mutable tags, revision N could silently start running newer code.
  image_tag_mutability = "IMMUTABLE"

  image_scanning_configuration {
    scan_on_push = true
  }

  tags = var.tags
}

# Lifecycle: expire untagged layers after a week (leftovers from failed pushes),
# and keep the newest ecr_keep_images tagged images.
#
# The keep count is also the rollback horizon. scripts/rollback.sh checks that
# the target revision's image still exists before switching to it.
resource "aws_ecr_lifecycle_policy" "this" {
  repository = aws_ecr_repository.this.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after 7 days"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = 7
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Keep the newest ${var.ecr_keep_images} images (the rollback horizon)"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = var.ecr_keep_images
        }
        action = { type = "expire" }
      },
    ]
  })
}
