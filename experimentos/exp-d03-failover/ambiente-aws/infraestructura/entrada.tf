# Entrada: Amazon API Gateway REST con API key y usage plan por socio,
# integrada al ALB interno mediante VPC Link V2 (decisión D13).

resource "aws_apigatewayv2_vpc_link" "v2" {
  name               = "${var.prefijo}-vpclink-v2"
  subnet_ids         = aws_subnet.publica[*].id
  security_group_ids = [aws_security_group.vpc_link.id]
}

resource "aws_api_gateway_rest_api" "solventa" {
  name        = "${var.prefijo}-api"
  description = "API de originacion para el experimento EXP-D03"

  endpoint_configuration {
    types = ["REGIONAL"]
  }
}

resource "aws_api_gateway_resource" "oferta" {
  rest_api_id = aws_api_gateway_rest_api.solventa.id
  parent_id   = aws_api_gateway_rest_api.solventa.root_resource_id
  path_part   = "oferta"
}

resource "aws_api_gateway_method" "oferta_post" {
  rest_api_id      = aws_api_gateway_rest_api.solventa.id
  resource_id      = aws_api_gateway_resource.oferta.id
  http_method      = "POST"
  authorization    = "NONE"
  api_key_required = true
}

resource "aws_api_gateway_integration" "oferta_post" {
  rest_api_id             = aws_api_gateway_rest_api.solventa.id
  resource_id             = aws_api_gateway_resource.oferta.id
  http_method             = aws_api_gateway_method.oferta_post.http_method
  type                    = "HTTP_PROXY"
  integration_http_method = "POST"
  connection_type         = "VPC_LINK"
  connection_id           = aws_apigatewayv2_vpc_link.v2.id
  integration_target      = aws_lb.principal.arn
  uri                     = "http://${aws_lb.principal.dns_name}/oferta"
  timeout_milliseconds    = 29000
}

resource "aws_api_gateway_deployment" "exp" {
  rest_api_id = aws_api_gateway_rest_api.solventa.id

  triggers = {
    redeployment = sha1(jsonencode([
      aws_api_gateway_resource.oferta.id,
      aws_api_gateway_method.oferta_post.id,
      aws_api_gateway_integration.oferta_post.id,
      aws_api_gateway_integration.oferta_post.integration_target,
      aws_api_gateway_integration.oferta_post.connection_id,
    ]))
  }

  lifecycle {
    create_before_destroy = true
  }

  depends_on = [aws_api_gateway_integration.oferta_post]
}

resource "aws_api_gateway_stage" "exp" {
  rest_api_id   = aws_api_gateway_rest_api.solventa.id
  deployment_id = aws_api_gateway_deployment.exp.id
  stage_name    = "exp"
}

resource "aws_api_gateway_usage_plan" "socio_prueba" {
  name = "${var.prefijo}-socio-prueba"

  api_stages {
    api_id = aws_api_gateway_rest_api.solventa.id
    stage  = aws_api_gateway_stage.exp.stage_name
  }

  throttle_settings {
    rate_limit  = var.usage_plan_tasa
    burst_limit = var.usage_plan_rafaga
  }

  quota_settings {
    limit  = 5000000
    period = "MONTH"
  }
}

resource "aws_api_gateway_api_key" "socio_prueba" {
  name = "${var.prefijo}-socio-prueba"
}

resource "aws_api_gateway_usage_plan_key" "socio_prueba" {
  key_id        = aws_api_gateway_api_key.socio_prueba.id
  key_type      = "API_KEY"
  usage_plan_id = aws_api_gateway_usage_plan.socio_prueba.id
}
