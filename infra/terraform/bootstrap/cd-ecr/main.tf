# Continuous delivery base: permanent ECR repositories, the IAM role that
# GitHub Actions assumes through OIDC to push to them, the release tag of main
# (SSM) and the role that deploys that tag to production-demo.
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
    # HU-W1.3-02: origination refuses to start with the charge enabled unless
    # it can reach a payment gateway and an e-mail provider, so both stubs
    # need a registry to be deployed from.
    "email",
    "payment-gateway",
  ])

  github_owner = split("/", var.github_repository)[0]
  github_name  = split("/", var.github_repository)[1]
  github_sub   = "repo:${local.github_owner}@${var.github_owner_id}/${local.github_name}@${var.github_repository_id}"

  # production-demo/platform: its ECS cluster, the prefix of its IAM roles and
  # secrets (name = "solventa-demo") and its remote state key.
  demo_name      = "solventa-demo"
  demo_state_key = "production-demo/platform/terraform.tfstate"
  state_bucket   = "solventa-tfstate-368083581678"

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
      values   = [for b in sort(tolist(var.deploy_branches)) : "${local.github_sub}:ref:refs/heads/${b}"]
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

  # After publishing from main, the workflow records the release tag and checks
  # whether production-demo is up before asking for the deploy approval.
  statement {
    sid       = "RecordReleaseTag"
    actions   = ["ssm:PutParameter"]
    resources = [aws_ssm_parameter.production_demo_image_tag.arn]
  }

  # Up means the cluster exists and every service runs tasks: while up.sh
  # prepares the database the services sit at 0 (scripts/ci/demo-status.sh).
  statement {
    sid       = "FindDemoCluster"
    actions   = ["ecs:DescribeClusters", "ecs:ListServices"]
    resources = ["*"] # ListServices does not support resource-level permissions
  }

  statement {
    sid       = "ReadDemoServices"
    actions   = ["ecs:DescribeServices"]
    resources = ["arn:aws:ecs:${var.region}:${local.account_id}:service/${local.demo_name}/*"]
  }
}

resource "aws_iam_role_policy" "push" {
  name   = "push-images"
  role   = aws_iam_role.cd.id
  policy = data.aws_iam_policy_document.push.json
}

# --- Release tag of main -----------------------------------------------------------------

# Commit (12-character SHA) whose images were all published from main. The
# backend-cd workflow overwrites the value; scripts/environment/up.sh reads it
# to turn production-demo on with -var image_tag=<sha>. "none" means main has
# not been published yet.
resource "aws_ssm_parameter" "production_demo_image_tag" {
  name        = "/solventa/production-demo/image-tag"
  description = "Image tag (12-character commit SHA) of the last main release published by backend-cd."
  type        = "String"
  value       = "none"

  lifecycle {
    ignore_changes = [value]
  }
}

# --- production-demo deploy --------------------------------------------------------------

# Assumed only by the deploy job of backend-cd, which runs in the GitHub
# environment production-demo (manual approval). Its OIDC sub carries the
# environment instead of the branch.
data "aws_iam_policy_document" "deploy_trust" {
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

    condition {
      test     = "StringEquals"
      variable = "${local.oidc_url}:sub"
      values   = ["${local.github_sub}:environment:${var.deploy_environment}"]
    }
  }
}

resource "aws_iam_role" "deploy" {
  name                 = "solventa-github-actions-deploy"
  description          = "Assumed by the deploy job of backend-cd (GitHub OIDC, environment ${var.deploy_environment}) to run terraform apply of production-demo/platform with a new image tag."
  assume_role_policy   = data.aws_iam_policy_document.deploy_trust.json
  max_session_duration = 3600
}

# terraform plan refreshes every resource of the platform (network, ALB, RDS,
# Redis, SNS/SQS, S3, API Gateway, Cognito, IAM...): read-only access to all of
# them. The writes below are limited to what a new image tag changes.
resource "aws_iam_role_policy_attachment" "deploy_read" {
  role       = aws_iam_role.deploy.name
  policy_arn = "arn:aws:iam::aws:policy/ReadOnlyAccess"
}

data "aws_iam_policy_document" "deploy" {
  statement {
    sid       = "StateList"
    actions   = ["s3:ListBucket"]
    resources = ["arn:aws:s3:::${local.state_bucket}"]
  }

  # State and its native S3 lock file (use_lockfile).
  statement {
    sid       = "StateReadWrite"
    actions   = ["s3:GetObject", "s3:PutObject"]
    resources = ["arn:aws:s3:::${local.state_bucket}/${local.demo_state_key}", "arn:aws:s3:::${local.state_bucket}/${local.demo_state_key}.tflock"]
  }

  statement {
    sid       = "StateUnlock"
    actions   = ["s3:DeleteObject"]
    resources = ["arn:aws:s3:::${local.state_bucket}/${local.demo_state_key}.tflock"]
  }

  # The platform reads the state of the permanent user pool. ReadOnlyAccess
  # covers it too; explicit so the deploy does not depend on that.
  statement {
    sid       = "ReadIdentityState"
    actions   = ["s3:GetObject"]
    resources = ["arn:aws:s3:::${local.state_bucket}/bootstrap/identity/terraform.tfstate"]
  }

  # Parameters of the environment, such as the portal URL (/solventa/production-demo/web/...).
  # SecureString values also need the AWS managed key, only through SSM.
  statement {
    sid     = "ReadEnvironmentParameters"
    actions = ["ssm:GetParameter", "ssm:GetParameters", "ssm:GetParametersByPath"]
    # GetParametersByPath is authorized on the path itself (aws_ssm_parameters_by_path
    # reads /solventa/production-demo/web); the wildcard covers sub-paths and parameters.
    resources = [
      "arn:aws:ssm:${var.region}:${local.account_id}:parameter/solventa/production-demo",
      "arn:aws:ssm:${var.region}:${local.account_id}:parameter/solventa/production-demo/*",
    ]
  }

  statement {
    sid       = "DecryptEnvironmentParameters"
    actions   = ["kms:Decrypt"]
    resources = ["*"]

    condition {
      test     = "StringEquals"
      variable = "kms:ViaService"
      values   = ["ssm.${var.region}.amazonaws.com"]
    }
  }

  # Refresh of aws_secretsmanager_secret_version (ReadOnlyAccess does not read values).
  statement {
    sid       = "RefreshSecretValues"
    actions   = ["secretsmanager:GetSecretValue"]
    resources = ["arn:aws:secretsmanager:${var.region}:${local.account_id}:secret:${local.demo_name}/*"]
  }

  # A new image replaces the task definitions (register the new revision,
  # deregister the old one) and points the services to them.
  statement {
    sid       = "TaskDefinitions"
    actions   = ["ecs:RegisterTaskDefinition", "ecs:DeregisterTaskDefinition"]
    resources = ["*"] # neither action supports resource-level permissions
  }

  statement {
    sid       = "TagTaskDefinitions"
    actions   = ["ecs:TagResource"]
    resources = ["arn:aws:ecs:${var.region}:${local.account_id}:task-definition/${local.demo_name}-*"]
  }

  statement {
    sid       = "UpdateServices"
    actions   = ["ecs:UpdateService"]
    resources = ["arn:aws:ecs:${var.region}:${local.account_id}:service/${local.demo_name}/*"]
  }

  statement {
    sid       = "PassTaskRoles"
    actions   = ["iam:PassRole"]
    resources = ["arn:aws:iam::${local.account_id}:role/${local.demo_name}-*"]

    condition {
      test     = "StringEquals"
      variable = "iam:PassedToService"
      values   = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role_policy" "deploy" {
  name   = "deploy-production-demo"
  role   = aws_iam_role.deploy.id
  policy = data.aws_iam_policy_document.deploy.json
}
