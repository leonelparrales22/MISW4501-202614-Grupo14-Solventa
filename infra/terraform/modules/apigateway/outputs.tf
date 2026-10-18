output "rest_api_id" {
  description = "ID of the REST API."
  value       = aws_api_gateway_rest_api.this.id
}

output "stage_name" {
  description = "Name of the stage."
  value       = aws_api_gateway_stage.this.stage_name
}

output "invoke_url" {
  description = "Base URL of the stage (BASE_URL of the k6 scripts)."
  value       = aws_api_gateway_stage.this.invoke_url
}

output "usage_plan_ids" {
  description = "Usage plan ID per partner."
  value       = { for k, p in aws_api_gateway_usage_plan.partner : k => p.id }
}

output "api_key_ids" {
  description = "API key ID per partner (the values are in the sensitive output api_key_values)."
  value       = { for k, key in aws_api_gateway_api_key.partner : k => key.id }
}

output "api_key_values" {
  description = "API key value per partner, for the k6 scripts of the staging window. Test keys only."
  value       = { for k, key in aws_api_gateway_api_key.partner : k => key.value }
  sensitive   = true
}

output "public_paths" {
  description = "Paths of the public routes, without session or API key (HA-D02)."
  value       = [for k in sort(keys(local.public_resources)) : "/v1/${k}"]
}

output "cognito_authorizer_id" {
  description = "ID of the COGNITO_USER_POOLS authorizer of the human routes (HA-S01; null without cognito_user_pool_arns)."
  value       = one(aws_api_gateway_authorizer.cognito[*].id)
}

output "payment_tokenization_path" {
  description = "Path of the public card tokenization route (T5; null without payment_tokenization)."
  value       = local.payment_tokenization_enabled ? "/pasarela/tokens" : null
}
