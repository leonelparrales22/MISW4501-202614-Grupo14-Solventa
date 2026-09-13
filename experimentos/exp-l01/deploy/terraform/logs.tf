# Un grupo de logs por contenedor. Retención corta: esto es efímero.

resource "aws_cloudwatch_log_group" "orquestador" {
  name              = "/${local.nombre}/orquestador"
  retention_in_days = 3
}

resource "aws_cloudwatch_log_group" "fuentes" {
  name              = "/${local.nombre}/fuentes"
  retention_in_days = 3
}

resource "aws_cloudwatch_log_group" "k6" {
  name              = "/${local.nombre}/k6"
  retention_in_days = 3
}
