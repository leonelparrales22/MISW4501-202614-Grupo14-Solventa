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
