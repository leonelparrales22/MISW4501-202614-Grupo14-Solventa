# ECR repositories of the project images (T4). Used once, by
# bootstrap/cd-ecr: the repositories are permanent and shared by every
# environment, which only read them (one copy of each image, published by the
# backend-cd workflow).

resource "aws_ecr_repository" "this" {
  for_each = var.images

  name = "${var.prefix}/${each.key}"

  # Images are tagged with the commit SHA and never overwritten, so a deployed
  # tag always means the same bytes and a rollback is a redeploy of an old tag.
  image_tag_mutability = var.image_tag_mutability
  force_delete         = var.force_delete

  image_scanning_configuration {
    scan_on_push = true
  }

  encryption_configuration {
    encryption_type = "AES256"
  }
}

resource "aws_ecr_lifecycle_policy" "this" {
  for_each = aws_ecr_repository.this

  repository = each.value.name

  policy = jsonencode({
    rules = [
      {
        rulePriority = 1
        description  = "Expire untagged images after ${var.untagged_expiry_days} days"
        selection = {
          tagStatus   = "untagged"
          countType   = "sinceImagePushed"
          countUnit   = "days"
          countNumber = var.untagged_expiry_days
        }
        action = { type = "expire" }
      },
      {
        rulePriority = 2
        description  = "Keep only the ${var.images_to_keep} most recent images"
        selection = {
          tagStatus   = "any"
          countType   = "imageCountMoreThan"
          countNumber = var.images_to_keep
        }
        action = { type = "expire" }
      },
    ]
  })
}
