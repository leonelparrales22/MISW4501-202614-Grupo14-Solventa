# Continuous delivery base (image publishing): permanent ECR repositories and
# the IAM role that GitHub Actions assumes through OIDC to push to them.
#
# No long-lived AWS keys are stored in GitHub: the workflow exchanges its OIDC
# token for short-lived credentials, and the trust policy only accepts runs of
# the configured branches of this repository (never pull requests).
#
# Applied once and permanent, like bootstrap/remote-state. The environments
# (environments/*/platform) read these repositories and deploy the images to ECS.

locals {
  account_id = "368083581678"

  # Same image names as the Dockerfiles of services/ and stubs/.
  images = toset([
    "audit",
    "claims",
    "notifications",
    "origination",
    "partners",
    "kyc",
    "opendata",
    "openfinance",
  ])

  github_owner = split("/", var.github_repository)[0]
  github_name  = split("/", var.github_repository)[1]

  oidc_url          = "token.actions.githubusercontent.com"
  oidc_provider_arn = var.create_oidc_provider ? aws_iam_openid_connect_provider.github[0].arn : var.existing_oidc_provider_arn

  # Shared and permanent resource: no Story and no TTL tags.
  default_tags = {
    Project = "solventa"
    Owner   = "nicolas"
    Env     = "shared"
    Purpose = "cd-image-publishing"
  }
}

# --- Registries -------------------------------------------------------------------------

# Immutable, scanned repositories with their lifecycle policy (modules/ecr, T4).
# The environments (environments/*/platform) only read them.
module "ecr" {
  source = "../../modules/ecr"

  images               = local.images
  untagged_expiry_days = var.untagged_expiry_days
  images_to_keep       = var.images_to_keep
}

# The repositories were created here before modules/ecr existed (2026-10-11).
# These blocks move them in the state, so the plan neither destroys nor
# recreates them (an immutable repository full of images).
moved {
  from = aws_ecr_repository.this
  to   = module.ecr.aws_ecr_repository.this
}

moved {
  from = aws_ecr_lifecycle_policy.this
  to   = module.ecr.aws_ecr_lifecycle_policy.this
}

# --- GitHub Actions identity -------------------------------------------------------------

resource "aws_iam_openid_connect_provider" "github" {
  count = var.create_oidc_provider ? 1 : 0

  url            = "https://${local.oidc_url}"
  client_id_list = ["sts.amazonaws.com"]
}

data "aws_iam_policy_document" "trust" {
  statement {
    actions = ["sts:AssumeRoleWithWebIdentity"]

    principals {
      type        = "Federated"
      identifiers = [local.oidc_provider_arn]
    }

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_url}:aud"
      values   = ["sts.amazonaws.com"]
    }

    # Exact match on the ref: only runs on the deploy branches of this repository.
    # The repository uses immutable subject claims, so GitHub embeds the numeric IDs
    # of the owner and the repository in the sub:
    #   repo:<owner>@<owner_id>/<name>@<repo_id>:ref:refs/heads/<branch>
    # The IDs survive a rename and cannot be inherited by a new repository that
    # reuses the old name.
    condition {
      test     = "StringEquals"
      variable = "${local.oidc_url}:sub"
      values   = [for b in sort(tolist(var.deploy_branches)) : "repo:${local.github_owner}@${var.github_owner_id}/${local.github_name}@${var.github_repository_id}:ref:refs/heads/${b}"]
    }
  }
}

resource "aws_iam_role" "cd" {
  name                 = "solventa-github-actions-ecr"
  description          = "Assumed by the backend-cd workflow (GitHub OIDC) to push images to the solventa/* ECR repositories."
  assume_role_policy   = data.aws_iam_policy_document.trust.json
  max_session_duration = 3600
}

data "aws_iam_policy_document" "push" {
  # GetAuthorizationToken does not support resource-level permissions.
  statement {
    sid       = "EcrLogin"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }

  statement {
    sid = "PushImages"
    actions = [
      "ecr:BatchCheckLayerAvailability",
      # buildx reads manifests while pushing (existing tags and attestations).
      "ecr:BatchGetImage",
      "ecr:CompleteLayerUpload",
      "ecr:DescribeImages",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ]
    resources = values(module.ecr.repository_arns)
  }
}

resource "aws_iam_role_policy" "push" {
  name   = "push-images"
  role   = aws_iam_role.cd.id
  policy = data.aws_iam_policy_document.push.json
}
