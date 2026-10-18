# Persistent platform of an environment (T4): everything the five services
# need in AWS, composed from the modules next to this one. Called by
# environments/staging/platform and environments/production-demo/platform,
# which only set the per-environment values.
#
#   client --HTTPS--> API Gateway REST (usage plans + API keys per partner,
#     Cognito authorizer on the human routes, public signed document link)
#     --VPC link V2--> internal ALB (EXP-D03 health checks, 30 s drain)
#     --> ECS Fargate services --> RDS PostgreSQL (one database per service),
#         ElastiCache Redis, SNS/SQS with DLQs, S3
#     --Cloud Map--> stubs of the external providers (Open Finance, Open Data, KYC,
#                    e-mail and payment gateway)
#
# The ECR repositories are permanent and shared (bootstrap/cd-ecr): this module
# only reads them.

data "aws_caller_identity" "current" {}

# Region of the AWS SDK of the services (AWS_REGION), explicit rather than
# discovered from the task metadata.
data "aws_region" "current" {}

locals {
  account_id = data.aws_caller_identity.current.account_id
  namespace  = "${var.name}.local"

  # Fixed facts of each service: container port, ALB rule priority and the
  # paths it owns, and the AWS resources it uses. Only origination has
  # business routes today; the other prefixes are reserved for their stories.
  services = {
    origination = {
      priority = 1
      paths = [
        "/v1/risk-profiles*", "/v1/offers*", "/v1/acceptances*", "/v1/assisted-quotes*",
        "/v1/customers*", "/v1/policies*", "/v1/documents*",
        # Advisor inbox (HU-W1.1-02) and issuance exceptions of operations (HA-D02).
        "/v1/review-requests*", "/v1/issuance-exceptions*",
      ]
      buckets   = ["documents"]
      publishes = ["origination"]
      consumes  = ["origination"]
    }
    claims = {
      priority  = 2
      paths     = ["/v1/claims*"]
      buckets   = ["evidence"]
      publishes = ["claims"]
      consumes  = []
    }
    partners = {
      priority  = 3
      paths     = ["/v1/partners*"]
      buckets   = []
      publishes = []
      consumes  = []
    }
    audit = {
      priority  = 4
      paths     = ["/v1/audit*"]
      buckets   = []
      publishes = []
      consumes  = ["audit"]
    }
    notifications = {
      priority  = 5
      paths     = ["/v1/notifications*"]
      buckets   = []
      publishes = []
      consumes  = ["notifications"]
    }
  }

  service_port = 8080

  # Stubs of the external providers, reached by origination through Cloud Map
  # (same ports as stubs/ and tests/load-k6/ha-l01).
  stubs = {
    openfinance = 9000
    opendata    = 9010
    kyc         = 9020
    # HU-W1.3-02: without a payment gateway the premium charge has nobody to
    # charge, and origination requires an e-mail provider whenever the charge
    # is enabled (decision D-16 b), so it does not start without both.
    email             = 9030
    "payment-gateway" = 9040
  }

  images = setunion(keys(local.services), keys(local.stubs))

  # Events of docs/events/README.md: one topic per publishing domain and one
  # queue per consumer.
  topics = toset(["origination", "claims"])
  queues = {
    origination   = { topics = ["origination"] } # contractual document (HU-W1.3-03)
    audit         = { topics = ["origination", "claims"] }
    notifications = { topics = ["origination", "claims"] }
  }

  buckets = toset(["documents", "evidence"])
}

# --- Images (permanent repositories of bootstrap/cd-ecr) ----------------------------

data "aws_ecr_repository" "this" {
  for_each = local.images

  name = "solventa/${each.key}"
}

# Without image_tag, each service runs the most recent image of its repository
# (pinned by digest, so a later push does not change a running task).
data "aws_ecr_image" "latest" {
  for_each = var.image_tag == null ? setsubtract(local.images, keys(var.image_tag_overrides)) : toset([])

  repository_name = data.aws_ecr_repository.this[each.key].name
  most_recent     = true
}

locals {
  image_uris = {
    for i in local.images : i => (
      contains(keys(var.image_tag_overrides), i) ? "${data.aws_ecr_repository.this[i].repository_url}:${var.image_tag_overrides[i]}" :
      var.image_tag != null ? "${data.aws_ecr_repository.this[i].repository_url}:${var.image_tag}" :
      "${data.aws_ecr_repository.this[i].repository_url}@${data.aws_ecr_image.latest[i].image_digest}"
    )
  }
}

# --- Network --------------------------------------------------------------------------------

module "network" {
  source = "../network"

  name       = var.name
  cidr_block = var.vpc_cidr
  nat_mode   = var.nat_mode
}

# --- Edge: API Gateway, VPC link and internal ALB -----------------------------------------

resource "aws_security_group" "vpc_link" {
  name        = "${var.name}-vpc-link"
  description = "VPC link V2 of the API Gateway"
  vpc_id      = module.network.vpc_id
  tags        = { Name = "${var.name}-vpc-link" }
}

resource "aws_vpc_security_group_egress_rule" "vpc_link" {
  security_group_id = aws_security_group.vpc_link.id
  ip_protocol       = "tcp"
  from_port         = 80
  to_port           = 80
  cidr_ipv4         = module.network.vpc_cidr_block
}

resource "aws_apigatewayv2_vpc_link" "this" {
  name               = var.name
  subnet_ids         = module.network.private_subnet_ids
  security_group_ids = [aws_security_group.vpc_link.id]
}

module "alb" {
  source = "../alb"

  name                       = var.name
  vpc_id                     = module.network.vpc_id
  vpc_cidr_block             = module.network.vpc_cidr_block
  subnet_ids                 = module.network.private_subnet_ids
  ingress_security_group_ids = { vpc_link = aws_security_group.vpc_link.id }
  deletion_protection        = var.deletion_protection

  targets = {
    for k, s in local.services : k => {
      port          = local.service_port
      priority      = s.priority
      path_patterns = s.paths
    }
  }
}

module "cognito" {
  source = "../cognito"

  name                = var.name
  deletion_protection = var.deletion_protection

  # Test users of a staging window (empty by default; production-demo never sets them).
  seed_users         = var.seed_users
  seed_user_password = var.seed_user_password
}

# Partner routes need an API key and get the usage plan of their partner
# (decision D-7: 100 req/s, burst 200). Human routes go through the Cognito
# authorizer, which validates the JWT before the request reaches the service.
# The public routes (the signed link of the contractual document, HA-D02) ask
# for neither and are throttled at the stage. Every media type passes as binary
# (decision D-4), so the PDF reaches the browser, curl and the web client
# intact; JSON goes through the HTTP_PROXY integrations unchanged.
module "apigateway" {
  source = "../apigateway"

  name                   = var.name
  stage_name             = var.environment
  backend_base_url       = "http://${module.alb.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = module.alb.arn
  cache_ttl_seconds      = var.cache_ttl_seconds
  partners               = var.partners
  cognito_user_pool_arns = [module.cognito.user_pool_arn]
  human_routes           = var.human_routes
  public_routes          = var.public_routes
  binary_media_types     = ["*/*"]
  cors_allow_origin      = var.cors_allow_origin
}

# --- Data -----------------------------------------------------------------------------------

module "rds" {
  source = "../rds"

  name                       = var.name
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.private_subnet_ids
  allowed_security_group_ids = { tasks = module.ecs.tasks_security_group_id }
  databases                  = toset(keys(local.services))
  instance_class             = var.rds_instance_class
  multi_az                   = var.rds_multi_az
  backup_retention_days      = var.rds_backup_retention_days
  deletion_protection        = var.deletion_protection
  skip_final_snapshot        = var.ephemeral
}

module "redis" {
  source = "../elasticache"

  name                       = var.name
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.private_subnet_ids
  allowed_security_group_ids = { tasks = module.ecs.tasks_security_group_id }
  node_type                  = var.redis_node_type
  multi_az                   = var.redis_multi_az
}

module "messaging" {
  source = "../messaging"

  name   = var.name
  topics = local.topics
  queues = local.queues
}

module "buckets" {
  source   = "../s3"
  for_each = local.buckets

  bucket        = "${var.name}-${each.key}-${local.account_id}"
  force_destroy = var.ephemeral
}

# Key that signs the links of the contractual document (HU-W1.3-03): only
# origination reads it, from Secrets Manager. Like the database passwords, the
# value also lands in the encrypted remote state of T4.
resource "random_password" "document_link" {
  length  = 48
  special = false
}

# One secret per service with its own login role, password and DATABASE_URL:
# a service can only reach its own database. Origination also gets the key of
# its document links (HA-D02, AWS phase).
module "secrets" {
  source = "../secrets"

  prefix                  = var.name
  recovery_window_in_days = var.ephemeral ? 0 : 7
  secrets = merge(
    { for s in keys(local.services) : "${s}/database" => "PostgreSQL credentials and DATABASE_URL of ${s} (only its own database)" },
    { "origination/document-link" = "DOCUMENT_LINK_SECRET of origination: signs the links of the contractual document (HU-W1.3-03)" },
  )

  values = merge(
    {
      for s, db in module.rds.databases : "${s}/database" => jsonencode({
        username     = db.username
        password     = module.rds.passwords[s]
        dbname       = db.database
        host         = module.rds.address
        port         = module.rds.port
        DATABASE_URL = "postgres://${db.username}:${module.rds.passwords[s]}@${module.rds.address}:${module.rds.port}/${db.database}?sslmode=require"
      })
    },
    { "origination/document-link" = jsonencode({ DOCUMENT_LINK_SECRET = random_password.document_link.result }) },
  )
}

# --- Compute ------------------------------------------------------------------------------

locals {
  # Creates (or updates) the login role and the database of every service, and
  # removes the default CONNECT of PUBLIC, so each role only reaches its own
  # database. Idempotent: run it after the first apply and after any password
  # change. psql quotes every identifier and literal with format(%I, %L).
  db_init_script = <<-EOT
    set -eu
    for db in $DATABASES; do
      role="$${db}_app"
      eval "pw=\$PASSWORD_$db"
      psql -v ON_ERROR_STOP=1 -v db="$db" -v role="$role" -v pw="$pw" <<'SQL'
    SELECT format('CREATE ROLE %I LOGIN', :'role') WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'role') \gexec
    SELECT format('ALTER ROLE %I WITH LOGIN PASSWORD %L', :'role', :'pw') \gexec
    SELECT format('GRANT %I TO CURRENT_USER', :'role') \gexec
    SELECT format('CREATE DATABASE %I OWNER %I', :'db', :'role') WHERE NOT EXISTS (SELECT 1 FROM pg_database WHERE datname = :'db') \gexec
    SELECT format('ALTER DATABASE %I OWNER TO %I', :'db', :'role') \gexec
    SELECT format('REVOKE ALL ON DATABASE %I FROM PUBLIC', :'db') \gexec
    SQL
      echo "database $db ready, owned by $role"
    done
  EOT

  task_role_statements = {
    for k, s in local.services : k => concat(
      length(s.buckets) == 0 ? [] : [
        {
          sid       = "ObjectsOfItsBuckets"
          actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject"]
          resources = [for b in s.buckets : "${module.buckets[b].arn}/*"]
        },
        {
          sid       = "ListItsBuckets"
          actions   = ["s3:ListBucket"]
          resources = [for b in s.buckets : module.buckets[b].arn]
        },
      ],
      length(s.publishes) == 0 ? [] : [{
        sid       = "PublishItsEvents"
        actions   = ["sns:Publish"]
        resources = [for t in s.publishes : module.messaging.topic_arns[t]]
      }],
      length(s.consumes) == 0 ? [] : [{
        sid       = "ConsumeItsQueue"
        actions   = ["sqs:ReceiveMessage", "sqs:DeleteMessage", "sqs:ChangeMessageVisibility", "sqs:GetQueueAttributes"]
        resources = [for q in s.consumes : module.messaging.queue_arns[q]]
      }],
    )
  }

  service_environment = {
    # The values of the platform win over origination_environment, whose
    # validation already refuses them.
    origination = merge(var.origination_environment, {
      PORT             = tostring(local.service_port)
      REDIS_ADDR       = module.redis.primary_endpoint
      OPEN_FINANCE_URL = "http://openfinance.${local.namespace}:${local.stubs.openfinance}"
      OPEN_DATA_URL    = "http://opendata.${local.namespace}:${local.stubs.opendata}"
      KYC_URL          = "http://kyc.${local.namespace}:${local.stubs.kyc}"
      EMAIL_URL        = "http://email.${local.namespace}:${local.stubs.email}"
      # Setting this is what enables the premium charge (HA-D02).
      PAYMENT_GATEWAY_URL = "http://payment-gateway.${local.namespace}:${local.stubs["payment-gateway"]}"
      AUTH_ISSUER         = module.cognito.issuer
      AUTH_JWKS_URL       = module.cognito.jwks_url
      AUTH_CLIENT_IDS     = join(",", values(module.cognito.client_ids))
      # Contractual document (HU-W1.3-03) in AWS (HA-D02): the PDF goes to the
      # documents bucket, and the e-mail links open it through the public
      # GET /v1/documents/{token} of the API Gateway stage.
      DOCUMENTS_BUCKET          = module.buckets["documents"].bucket
      DOCUMENTS_PUBLIC_BASE_URL = module.apigateway.invoke_url
      AWS_REGION                = data.aws_region.current.region
    })
  }

  # Secrets Manager values of each service (the ECS secrets field): its
  # DATABASE_URL and, for origination, the key of the document links.
  service_secrets = {
    for k in keys(local.services) : k => merge(
      { DATABASE_URL = "${module.secrets.arns["${k}/database"]}:DATABASE_URL::" },
      k == "origination" ? { DOCUMENT_LINK_SECRET = "${module.secrets.arns["origination/document-link"]}:DOCUMENT_LINK_SECRET::" } : {},
    )
  }
}

module "ecs" {
  source = "../ecs"

  name                  = var.name
  vpc_id                = module.network.vpc_id
  subnet_ids            = module.network.app_subnet_ids
  assign_public_ip      = module.network.app_assign_public_ip
  alb_security_group_id = module.alb.security_group_id
  discovery_namespace   = local.namespace
  log_retention_days    = var.log_retention_days

  services = merge(
    {
      for k, s in local.services : k => {
        image                = local.image_uris[k]
        port                 = local.service_port
        cpu                  = var.service_sizing[k].cpu
        memory               = var.service_sizing[k].memory
        desired_count        = var.services_enabled ? var.service_sizing[k].desired_count : 0
        max_count            = var.services_enabled ? var.service_sizing[k].max_count : 0
        load_balanced        = true
        target_group_arn     = module.alb.target_group_arns[k]
        environment          = lookup(local.service_environment, k, { PORT = tostring(local.service_port) })
        secrets              = local.service_secrets[k]
        task_role_statements = local.task_role_statements[k]
      }
    },
    {
      for k, port in local.stubs : k => {
        image          = local.image_uris[k]
        port           = port
        cpu            = 256
        memory         = 512
        desired_count  = var.services_enabled ? lookup(var.stub_desired_count_overrides, k, var.stub_desired_count) : 0
        max_count      = var.services_enabled ? lookup(var.stub_desired_count_overrides, k, var.stub_desired_count) : 0
        discovery_name = k
        environment    = { PORT = tostring(port) }
      }
    },
  )

  tasks = {
    db-init = {
      image       = "public.ecr.aws/docker/library/postgres:16-alpine"
      entry_point = ["sh", "-c"]
      command     = [local.db_init_script]
      environment = {
        PGHOST     = module.rds.address
        PGPORT     = tostring(module.rds.port)
        PGDATABASE = "postgres"
        PGSSLMODE  = "require"
        DATABASES  = join(" ", sort(keys(local.services)))
      }
      secrets = merge(
        {
          PGUSER     = "${module.rds.master_user_secret_arn}:username::"
          PGPASSWORD = "${module.rds.master_user_secret_arn}:password::"
        },
        { for s in keys(local.services) : "PASSWORD_${s}" => "${module.secrets.arns["${s}/database"]}:password::" },
      )
    }
  }
}
