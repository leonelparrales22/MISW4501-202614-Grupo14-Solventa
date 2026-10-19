# None of these values is secret. user_pool_id, user_pool_arn, issuer,
# jwks_url and client_ids are read by environments/production-demo/platform and
# environments/staging/platform (terraform_remote_state): keep their names.

output "region" {
  description = "Region of the user pool (the portal and the app need it to call Cognito)."
  value       = var.region
}

output "user_pool_id" {
  description = "ID of the permanent user pool."
  value       = module.cognito.user_pool_id
}

output "user_pool_arn" {
  description = "ARN of the user pool (provider of the API Gateway COGNITO_USER_POOLS authorizer)."
  value       = module.cognito.user_pool_arn
}

output "issuer" {
  description = "Token issuer (AUTH_ISSUER of the services)."
  value       = module.cognito.issuer
}

output "jwks_url" {
  description = "Public keys of the user pool (AUTH_JWKS_URL of the services)."
  value       = module.cognito.jwks_url
}

output "client_ids" {
  description = "App client ID per channel (web, mobile); together they are AUTH_CLIENT_IDS."
  value       = module.cognito.client_ids
}

output "group_names" {
  description = "Role groups of the user pool (cognito:groups claim)."
  value       = module.cognito.group_names
}

output "test_users" {
  description = "E-mail of the permanent test user of each role group."
  value       = { for u in values(local.test_users) : u.group => u.email }
}
