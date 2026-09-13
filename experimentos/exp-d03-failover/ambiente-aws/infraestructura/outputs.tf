output "region" {
  value = var.region
}

output "ecr_repositorio" {
  value = aws_ecr_repository.stub.repository_url
}

output "cluster" {
  value = aws_ecs_cluster.principal.name
}

output "servicio" {
  value = aws_ecs_service.stub.name
}

output "target_group_arn" {
  value = aws_lb_target_group.stub.arn
}

output "alb_dns" {
  value = aws_lb.principal.dns_name
}

output "api_url" {
  description = "URL de invocación de la etapa (k6 llama a <api_url>/oferta)."
  value       = aws_api_gateway_stage.exp.invoke_url
}

output "api_key" {
  value     = aws_api_gateway_api_key.socio_prueba.value
  sensitive = true
}

output "admin_token" {
  value     = random_password.admin_token.result
  sensitive = true
}

output "rds_identificador" {
  value = aws_db_instance.originacion.identifier
}

output "rds_endpoint" {
  value = aws_db_instance.originacion.address
}

output "configuracion" {
  description = "Parámetros de la ejecución, para dejarlos en la evidencia."
  value = {
    hc_intervalo         = var.hc_intervalo
    hc_timeout           = var.hc_timeout
    hc_umbral_unhealthy  = var.hc_umbral_unhealthy
    hc_umbral_healthy    = var.hc_umbral_healthy
    deregistration_delay = var.deregistration_delay
    pool_max_lifetime    = var.pool_max_lifetime
    pool_max_idle_time   = var.pool_max_idle_time
    health_mode          = var.health_mode
    alb_interno          = var.alb_interno
  }
}
