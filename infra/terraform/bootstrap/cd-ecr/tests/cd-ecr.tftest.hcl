# Configuration tests of the CD base (image publishing). They run with a mocked
# AWS provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

# Immutability, scanning, force_delete and the lifecycle policy are asserted by
# the tests of modules/ecr; the bootstrap keeps the module defaults.
run "one_repository_per_image" {
  command = plan

  assert {
    condition     = length(module.ecr.repository_names) == 10
    error_message = "There must be one repository for each of the 5 services and 5 stubs with a Dockerfile."
  }

  assert {
    condition     = module.ecr.repository_names["origination"] == "solventa/origination"
    error_message = "Repositories are named solventa/<image>."
  }
}

run "oidc_provider_is_created_by_default" {
  command = plan

  assert {
    condition     = length(aws_iam_openid_connect_provider.github) == 1
    error_message = "The GitHub OIDC provider must be created by default."
  }

  assert {
    condition     = aws_iam_openid_connect_provider.github[0].url == "https://token.actions.githubusercontent.com"
    error_message = "The provider must be GitHub's token endpoint."
  }

  assert {
    condition     = aws_iam_role.cd.name == "solventa-github-actions-ecr"
    error_message = "The CD role name is referenced by the backend-cd workflow."
  }
}

run "existing_provider_is_reused" {
  command = plan

  variables {
    create_oidc_provider       = false
    existing_oidc_provider_arn = "arn:aws:iam::368083581678:oidc-provider/token.actions.githubusercontent.com"
  }

  assert {
    condition     = length(aws_iam_openid_connect_provider.github) == 0
    error_message = "No provider may be created when an existing one is supplied."
  }
}

run "existing_provider_arn_is_required_when_not_creating" {
  command = plan

  variables {
    create_oidc_provider = false
  }

  expect_failures = [var.existing_oidc_provider_arn]
}

run "region_is_validated" {
  command = plan

  variables {
    region = "us-east-1"
  }

  expect_failures = [var.region]
}

run "trust_policy_accepts_only_the_immutable_subject_of_the_deploy_branches" {
  command = plan

  assert {
    condition = toset(flatten([
      for c in data.aws_iam_policy_document.trust.statement[0].condition :
      c.values if c.variable == "token.actions.githubusercontent.com:sub"
      ])) == toset([
      "repo:leonelparrales22@52500190/MISW4501-202614-Grupo14-Solventa@1325296709:ref:refs/heads/develop",
      "repo:leonelparrales22@52500190/MISW4501-202614-Grupo14-Solventa@1325296709:ref:refs/heads/main",
    ])
    error_message = "The sub must be the immutable one (owner and repository IDs) and only for develop and main; a name-based sub is never emitted by this repository."
  }

  assert {
    condition = flatten([
      for c in data.aws_iam_policy_document.trust.statement[0].condition :
      c.values if c.variable == "token.actions.githubusercontent.com:aud"
    ]) == ["sts.amazonaws.com"]
    error_message = "The audience must be sts.amazonaws.com."
  }
}

run "role_can_only_push_to_the_image_repositories" {
  command = plan

  assert {
    condition = toset(flatten([
      for s in data.aws_iam_policy_document.push.statement : s.actions if s.sid == "PushImages"
      ])) == toset([
      "ecr:BatchCheckLayerAvailability",
      "ecr:BatchGetImage",
      "ecr:CompleteLayerUpload",
      "ecr:DescribeImages",
      "ecr:InitiateLayerUpload",
      "ecr:PutImage",
      "ecr:UploadLayerPart",
    ])
    error_message = "The role must be able to push (including the manifest reads buildx does) and nothing else: no delete, no layer download."
  }

  assert {
    condition = flatten([
      for s in data.aws_iam_policy_document.push.statement : s.actions if s.sid == "EcrLogin"
    ]) == ["ecr:GetAuthorizationToken"]
    error_message = "The only action allowed on every resource is GetAuthorizationToken."
  }
}

run "release_tag_parameter_is_created" {
  command = plan

  assert {
    condition     = aws_ssm_parameter.production_demo_image_tag.name == "/solventa/production-demo/image-tag"
    error_message = "The parameter name is read by backend-cd and scripts/environment/up.sh."
  }

  assert {
    condition     = aws_ssm_parameter.production_demo_image_tag.value == "none"
    error_message = "Until main is published the parameter must say none."
  }
}

run "deploy_role_is_separate_from_the_publish_role" {
  command = plan

  assert {
    condition     = aws_iam_role.deploy.name == "solventa-github-actions-deploy"
    error_message = "The deploy role name is referenced by the backend-cd workflow."
  }

  assert {
    condition     = aws_iam_role_policy_attachment.deploy_read.policy_arn == "arn:aws:iam::aws:policy/ReadOnlyAccess"
    error_message = "terraform plan needs to read every resource of the platform."
  }

  assert {
    condition = flatten([
      for c in data.aws_iam_policy_document.deploy_trust.statement[0].condition :
      c.values if c.variable == "token.actions.githubusercontent.com:sub"
      ]) == [
      "repo:leonelparrales22@52500190/MISW4501-202614-Grupo14-Solventa@1325296709:environment:production-demo",
    ]
    error_message = "Only jobs of the production-demo environment (manual approval) may deploy; never a branch run or a pull request."
  }
}
