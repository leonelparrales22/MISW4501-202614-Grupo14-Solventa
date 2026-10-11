output "user_pool_id" {
  description = "ID of the user pool."
  value       = aws_cognito_user_pool.this.id
}

output "user_pool_arn" {
  description = "ARN of the user pool (provider of the API Gateway COGNITO_USER_POOLS authorizer)."
  value       = aws_cognito_user_pool.this.arn
}

output "issuer" {
  description = "Token issuer (AUTH_ISSUER of the services, pkg/security)."
  value       = "https://${aws_cognito_user_pool.this.endpoint}"
}

output "jwks_url" {
  description = "Public keys of the user pool (AUTH_JWKS_URL of the services)."
  value       = "https://${aws_cognito_user_pool.this.endpoint}/.well-known/jwks.json"
}

output "client_ids" {
  description = "App client ID per channel (web, mobile); together they are AUTH_CLIENT_IDS."
  value       = { for k, c in aws_cognito_user_pool_client.this : k => c.id }
}

output "group_names" {
  description = "Role groups of the user pool (cognito:groups claim)."
  value       = sort(keys(aws_cognito_user_group.this))
}
