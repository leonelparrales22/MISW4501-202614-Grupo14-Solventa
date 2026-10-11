output "region" {
  description = "Region of the window (Cognito endpoint of the verification script)."
  value       = var.region
}

output "invoke_url" {
  description = "Base URL of the test API stage."
  value       = aws_api_gateway_stage.test.invoke_url
}

output "human_url" {
  description = "Human route behind the Cognito authorizer."
  value       = "${aws_api_gateway_stage.test.invoke_url}/ha-s01/humana"
}

output "partner_url" {
  description = "Partner route behind the API key and usage plan."
  value       = "${aws_api_gateway_stage.test.invoke_url}/ha-s01/socio"
}

output "user_pool_id" {
  description = "ID of the user pool."
  value       = module.cognito.user_pool_id
}

output "issuer" {
  description = "Token issuer (AUTH_ISSUER)."
  value       = module.cognito.issuer
}

output "jwks_url" {
  description = "Public keys of the user pool (AUTH_JWKS_URL)."
  value       = module.cognito.jwks_url
}

output "client_ids" {
  description = "App client ID per channel (web, mobile)."
  value       = module.cognito.client_ids
}

output "web_client_id" {
  description = "App client ID of the web channel (used by the verification script)."
  value       = module.cognito.client_ids["web"]
}

output "mobile_client_id" {
  description = "App client ID of the mobile channel (pkg/security staging test: AUTH_STAGING_CLIENT_IDS)."
  value       = module.cognito.client_ids["mobile"]
}

output "seed_user_emails" {
  description = "Email of each seed user (advisor, operations, partner, insured)."
  value       = { for k, u in var.seed_users : k => u.email }
}

output "api_key_id" {
  description = "ID of the test partner API key (value in the sensitive output api_key_value)."
  value       = aws_api_gateway_api_key.partner.id
}

output "api_key_value" {
  description = "Test partner API key of the window; it is destroyed with it."
  value       = aws_api_gateway_api_key.partner.value
  sensitive   = true
}

# --- Real modules/apigateway -----------------------------------------------------------

output "module_invoke_url" {
  description = "Base URL of the stage of the real apigateway module."
  value       = module.apigateway.invoke_url
}

output "module_human_url" {
  description = "Human route of the real module (Cognito authorizer, HTTP_PROXY to the test API backend)."
  value       = "${module.apigateway.invoke_url}/v1/${local.module_check_route}"
}

output "module_partner_url" {
  description = "Partner route of the real module (API key only; its backend does not exist in the window)."
  value       = "${module.apigateway.invoke_url}/v1/risk-profiles/${local.module_check_route}?consent_active=true"
}

output "module_api_key_value" {
  description = "Test API key of socio-a in the real module; it is destroyed with the window."
  value       = module.apigateway.api_key_values["socio-a"]
  sensitive   = true
}
