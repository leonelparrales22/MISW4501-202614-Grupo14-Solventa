# Persistent platform of an environment (T4): everything the five services
# need in AWS, composed from the modules next to this one. Called by
# environments/staging/platform and environments/production-demo/platform,
# which only set the per-environment values.
#
#   client --HTTPS--> API Gateway REST (usage plans + API keys per partner,
#     Cognito authorizer (own or external user pool) on the human routes,
#     public signed document link)
#     --VPC link V2--> internal ALB (EXP-D03 health checks, 30 s drain)
#     --> ECS Fargate services --> RDS PostgreSQL (one database per service),
#         ElastiCache Redis, SNS/SQS with DLQs, S3
#     --Cloud Map--> stubs of the external providers (Open Finance, Open Data, KYC,
#                    e-mail, payment gateway, policy core and push)
#   browser --POST /pasarela/tokens (public, T5)--> API Gateway --> internal ALB
#     /tokens --> payment gateway stub (card tokenization of screen E4)
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
        # Issuance monitor of operations (HU-W1.3-04).
        "/v1/issuances*",
        # Sign-up of the insured (HU-M1.1-01). The API Gateway publishes the
        # route without a session, but the load balancer has to send it
        # somewhere: without this rule it answers 404 and the mobile sign-up
        # never reaches the service.
        "/v1/registrations*",
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
      priority = 3
      paths    = ["/v1/partners*"]
      buckets  = []
      # HU-W3.1-01: the credential events of the partners go to audit.
      publishes = ["partners"]
      consumes  = []
    }
    audit = {
      priority = 4
      # /v1/audit* for the banks (API key) and /v1/consents* for the people
      # (Cognito, HU-M1.3-01): one prefix per resource (decision D-3 a).
      paths     = ["/v1/audit*", "/v1/consents*"]
      buckets   = []
      publishes = []
      consumes  = ["audit"]
    }
    notifications = {
      priority = 5
      # HU-M3.1-01: the notification center of the insured and the
      # registration of their devices, both human routes of the mobile app.
      paths     = ["/v1/notifications*", "/v1/devices*"]
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
    # HU-W2.1-01: the policy core that claims reads inside the VPC. The browser
    # never calls it, so it is not in lb_stubs.
    "policy-core" = 9060
    # HU-M3.1-01: the push provider that notifications sends to inside the VPC
    # (Firebase Cloud Messaging in the architecture; a stub by decision of the
    # team). The browser never calls it, so it is not in lb_stubs.
    push = 9070
  }

  # Stubs that the browser also reaches through the API Gateway, so they sit
  # behind the ALB besides Cloud Map: only the card tokenization of the payment
  # gateway (T5, decision D-4), on /tokens alone (the ALB answers 404 to
  # /charges and /debug/*, and the gateway does not even publish them).
  lb_stubs = {
    "payment-gateway" = { priority = 6, paths = ["/tokens"] }
  }

  # Services that another service calls directly inside the VPC, so they are
  # also registered in Cloud Map without leaving the ALB: only audit, whose
  # consent verification origination asks before profiling (HU-W1.2-01) and
  # before a personalized offer (HU-W1.2-03). T5, decision D-21 (a).
  discoverable_services = ["audit"]

  images = setunion(keys(local.services), keys(local.stubs))

  # Events of docs/events/README.md: one topic per publishing domain and one
  # queue per consumer.
  topics = toset(["origination", "claims", "partners"])
  queues = {
    origination = { topics = ["origination"] } # contractual document (HU-W1.3-03)
    # partners: audit record of the partner credentials (HU-W3.1-01).
    audit         = { topics = ["origination", "claims", "partners"] }
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

  targets = merge(
    {
      for k, s in local.services : k => {
        port          = local.service_port
        priority      = s.priority
        path_patterns = s.paths
      }
    },
    {
      for k, s in local.lb_stubs : k => {
        port          = local.stubs[k]
        priority      = s.priority
        path_patterns = s.paths
      }
    },
  )
}

module "cognito" {
  source = "../cognito"
  count  = var.existing_cognito == null ? 1 : 0

  name                = var.name
  deletion_protection = var.deletion_protection

  # Test users, only when the platform creates its own user pool (existing_cognito
  # = null); empty by default. staging and production-demo never set them: they
  # use the permanent test users of bootstrap/identity.
  seed_users         = var.seed_users
  seed_user_password = var.seed_user_password
}

# The user pool was created without count before existing_cognito existed: a
# state with that address keeps its pool when existing_cognito is null.
moved {
  from = module.cognito
  to   = module.cognito[0]
}

locals {
  # User pool of the human channels: the permanent one received in existing_cognito
  # (bootstrap/identity, T4) or the one this module creates for the environment.
  cognito = var.existing_cognito != null ? var.existing_cognito : {
    user_pool_id  = one(module.cognito[*].user_pool_id)
    user_pool_arn = one(module.cognito[*].user_pool_arn)
    issuer        = one(module.cognito[*].issuer)
    jwks_url      = one(module.cognito[*].jwks_url)
    client_ids    = one(module.cognito[*].client_ids)
  }
}

# Partner routes need an API key and get the usage plan of their partner
# (decision D-7: 100 req/s, burst 200). Human routes go through the Cognito
# authorizer, which validates the JWT before the request reaches the service.
# The public routes (the signed link of the contractual document, HA-D02) ask
# for neither and are throttled at the stage. The consent of HU-W1.2-01 and
# POST /v1/offers of HU-W1.2-03 are partner routes on exact paths (T5); the
# consent verification of audit is never published. Every media type passes as binary
# (decision D-4), so the PDF reaches the browser, curl and the web client
# intact; JSON goes through the HTTP_PROXY integrations unchanged. The card
# tokenization of the payment gateway (T5) is public too: POST /pasarela/tokens
# goes to /tokens of the stub behind the ALB, throttled at the stage.
module "apigateway" {
  source = "../apigateway"

  name                   = var.name
  stage_name             = var.environment
  backend_base_url       = "http://${module.alb.dns_name}"
  vpc_link_v2_id         = aws_apigatewayv2_vpc_link.this.id
  integration_target_arn = module.alb.arn
  cache_ttl_seconds      = var.cache_ttl_seconds
  partners               = var.partners
  cognito_user_pool_arns = [local.cognito.user_pool_arn]
  human_routes           = var.human_routes
  partner_routes         = var.partner_routes
  human_subroutes        = var.human_subroutes
  public_routes          = var.public_routes
  binary_media_types     = ["*/*"]
  cors_allow_origin      = var.cors_allow_origin
  payment_tokenization   = {}
  registration_route     = {}
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

  # HU-W2.1-01: the web portal uploads the supports of a claim straight to the
  # evidence bucket with signed URLs, so its origin must be allowed. The mobile
  # app is not a browser and needs no CORS.
  cors_allowed_origins = each.key == "evidence" && var.portal_base_url != null ? [var.portal_base_url] : []
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
      # The sign-up of the insured creates the account (HU-M1.1-01). Only
      # origination, only on this user pool, and only the three actions the
      # sign-up performs: it cannot read, list or delete users.
      k != "origination" ? [] : [{
        sid       = "CreateInsuredAccounts"
        actions   = ["cognito-idp:AdminCreateUser", "cognito-idp:AdminSetUserPassword", "cognito-idp:AdminAddUserToGroup"]
        resources = [local.cognito.user_pool_arn]
      }],
      # HU-W3.1-01: partners creates the usage plan and the API keys of each
      # partner on the stage of this API and reads their daily usage. Only
      # the API keys, the usage plans (and their keys and usage) and a read of
      # the stage: it cannot touch the resources, methods or deployments.
      # One resource per statement (/apikeys* covers /apikeys and its keys,
      # /usageplans* the plans, their keys and their usage).
      k != "partners" ? [] : [{
        sid       = "ManageThePartnerApiKeys"
        actions   = ["apigateway:GET", "apigateway:POST", "apigateway:PATCH", "apigateway:DELETE"]
        resources = ["arn:aws:apigateway:${data.aws_region.current.region}::/apikeys*"]
      }],
      k != "partners" ? [] : [{
        sid       = "ManageThePartnerUsagePlans"
        actions   = ["apigateway:GET", "apigateway:POST", "apigateway:PATCH", "apigateway:DELETE"]
        resources = ["arn:aws:apigateway:${data.aws_region.current.region}::/usageplans*"]
      }],
      k != "partners" ? [] : [{
        sid       = "ReadTheStageOfThePartnerPlans"
        actions   = ["apigateway:GET"]
        resources = ["arn:aws:apigateway:${data.aws_region.current.region}::/restapis/${module.apigateway.rest_api_id}/stages/${module.apigateway.stage_name}"]
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
      # Setting this is what mounts the sign-up of the insured (HU-M1.1-01).
      COGNITO_USER_POOL_ID = local.cognito.user_pool_id
      AUTH_ISSUER          = local.cognito.issuer
      AUTH_JWKS_URL        = local.cognito.jwks_url
      AUTH_CLIENT_IDS      = join(",", values(local.cognito.client_ids))
      # Consent verification of HU-W1.2-01 (and of the personalized offer of
      # HU-W1.2-03): audit through Cloud Map, never through the API Gateway,
      # where GET /v1/audit/consent-verifications is not published. If audit
      # does not answer within AUDIT_TIMEOUT_MS (300 ms by default) origination
      # fails closed. T5, decision D-21 (a).
      AUDIT_URL = "http://audit.${local.namespace}:${local.service_port}"
      # Contractual document (HU-W1.3-03) in AWS (HA-D02): the PDF goes to the
      # documents bucket, and the e-mail links open it through the public
      # GET /v1/documents/{token}: through the web portal when it exists (its
      # CloudFront distribution sends /v1/* to this stage, T5 decision D-11),
      # otherwise straight through the API Gateway stage.
      DOCUMENTS_BUCKET          = module.buckets["documents"].bucket
      DOCUMENTS_PUBLIC_BASE_URL = coalesce(var.portal_base_url, module.apigateway.invoke_url)
      AWS_REGION                = data.aws_region.current.region
      # Domain events (HU-M1.3-01): the outbox relay publishes CustomerLinked
      # to the topic of origination; the audit queue is subscribed to it.
      EVENTS_TOPIC_ARN = module.messaging.topic_arns["origination"]
      },
      # Links of the offer e-mails to the screens of the portal (T5, D-11);
      # without a portal the service keeps its own default.
      var.portal_base_url != null ? { OFFER_LINK_BASE_URL = var.portal_base_url } : {}
    )
    # HU-M1.3-01: the insured reads their consents (GET /v1/consents*, behind
    # the Cognito authorizer and verified again in the service) and audit
    # consumes the CustomerLinked events of its queue to recognize the holder.
    # With AUTH_* audit also mounts the evidence lookup of HU-W1.2-01, which
    # the gateway still does not publish (T5, decision D-23 c).
    # HU-M1.3-02: audit notifies each entity of a revocation through the
    # Open Finance stub, reached by Cloud Map like origination does (the
    # security group of the tasks already allows its port between tasks),
    # within the 5 minutes of RN-02. No topic, queue nor rule is added
    # (decision D-2 a).
    audit = {
      PORT                               = tostring(local.service_port)
      AWS_REGION                         = data.aws_region.current.region
      AUTH_ISSUER                        = local.cognito.issuer
      AUTH_JWKS_URL                      = local.cognito.jwks_url
      AUTH_CLIENT_IDS                    = join(",", values(local.cognito.client_ids))
      PARTNER_DISPLAY_NAMES              = jsonencode(var.partner_display_names)
      EVENTS_QUEUE_URL                   = module.messaging.queue_urls["audit"]
      OPEN_FINANCE_URL                   = "http://openfinance.${local.namespace}:${local.stubs.openfinance}"
      REVOCATION_NOTICE_DEADLINE_SECONDS = "300"
    }

    # HU-W3.1-01: the operations portal registers and approves the partners
    # and rotates their credentials (human routes, Cognito). The credentials
    # are API keys in a usage plan per partner on the stage of this API, so
    # the gateway enforces each quota and answers 429 only to that partner;
    # the credential events go to the partners topic, which audit consumes.
    partners = {
      PORT                          = tostring(local.service_port)
      AWS_REGION                    = data.aws_region.current.region
      ENVIRONMENT                   = var.environment
      AUTH_ISSUER                   = local.cognito.issuer
      AUTH_JWKS_URL                 = local.cognito.jwks_url
      AUTH_CLIENT_IDS               = join(",", values(local.cognito.client_ids))
      GATEWAY_REST_API_ID           = module.apigateway.rest_api_id
      GATEWAY_STAGE_NAME            = module.apigateway.stage_name
      CREDENTIAL_GRACE_PERIOD_HOURS = "24"
      EVENTS_TOPIC_ARN              = module.messaging.topic_arns["partners"]
    }

    # Claims serves the filing of the mobile assistant (HU-M2.2-01), which is
    # a human route: without the user pool there is no way to tell whose claim
    # it is and the route is not mounted. The evidence of HU-M2.1-01 goes to
    # the evidence bucket, and the service signs the upload with the task role.
    claims = {
      PORT            = tostring(local.service_port)
      AUTH_ISSUER     = local.cognito.issuer
      AUTH_JWKS_URL   = local.cognito.jwks_url
      AUTH_CLIENT_IDS = join(",", values(local.cognito.client_ids))
      EVIDENCE_BUCKET = module.buckets["evidence"].bucket
      AWS_REGION      = data.aws_region.current.region
      # Setting this is what mounts the web filing of HU-W2.1-01 (filing
      # options, supports and web filings). Without it claims logs that those
      # routes are not mounted and answers 404 to the portal.
      POLICY_CORE_URL = "http://policy-core.${local.namespace}:${local.stubs["policy-core"]}"
      # HU-W2.2-03: setting the gateway is what mounts the decision of a
      # claim, whose approval pays the indemnity; the e-mail provider tells
      # the insured a rejection and its reason.
      PAYMENT_GATEWAY_URL = "http://payment-gateway.${local.namespace}:${local.stubs["payment-gateway"]}"
      EMAIL_URL           = "http://email.${local.namespace}:${local.stubs.email}"
    }

    # HU-M3.1-01: the notification center of the insured and the push of the
    # automatic parametric payments. Without AUTH_* the center routes are not
    # mounted, without EVENTS_QUEUE_URL no payment is consumed and without
    # PUSH_PROVIDER_URL no push is sent (the notifications wait in the
    # center). The role already had ConsumeItsQueue on the notifications
    # queue, which is subscribed to the claims topic (T4).
    notifications = {
      PORT              = tostring(local.service_port)
      AWS_REGION        = data.aws_region.current.region
      AUTH_ISSUER       = local.cognito.issuer
      AUTH_JWKS_URL     = local.cognito.jwks_url
      AUTH_CLIENT_IDS   = join(",", values(local.cognito.client_ids))
      EVENTS_QUEUE_URL  = module.messaging.queue_urls["notifications"]
      PUSH_PROVIDER_URL = "http://push.${local.namespace}:${local.stubs.push}"
    }
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
        discovery_name       = contains(local.discoverable_services, k) ? k : null
        environment          = lookup(local.service_environment, k, { PORT = tostring(local.service_port) })
        secrets              = local.service_secrets[k]
        task_role_statements = local.task_role_statements[k]
      }
    },
    {
      for k, port in local.stubs : k => {
        image         = local.image_uris[k]
        port          = port
        cpu           = 256
        memory        = 512
        desired_count = var.services_enabled ? lookup(var.stub_desired_count_overrides, k, var.stub_desired_count) : 0
        max_count     = var.services_enabled ? lookup(var.stub_desired_count_overrides, k, var.stub_desired_count) : 0
        # Origination keeps reaching every stub through Cloud Map; the ones of
        # lb_stubs are also behind the ALB (T5).
        discovery_name   = k
        load_balanced    = contains(keys(local.lb_stubs), k)
        target_group_arn = contains(keys(local.lb_stubs), k) ? module.alb.target_group_arns[k] : null
        environment      = { PORT = tostring(port) }
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
