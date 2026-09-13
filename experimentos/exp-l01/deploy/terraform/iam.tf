# Dos roles, como pide ECS:
#   - ejecucion: lo usa el agente de ECS para bajar la imagen y escribir logs
#   - tarea:     lo usa el contenedor. Acá no necesita nada, pero queda
#                declarado por si algún día hay que darle permisos.

data "aws_iam_policy_document" "asume_ecs" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ecs-tasks.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "ejecucion" {
  name               = "${local.nombre}-ejecucion"
  assume_role_policy = data.aws_iam_policy_document.asume_ecs.json
}

resource "aws_iam_role_policy_attachment" "ejecucion" {
  role       = aws_iam_role.ejecucion.name
  policy_arn = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
}

resource "aws_iam_role" "tarea" {
  name               = "${local.nombre}-tarea"
  assume_role_policy = data.aws_iam_policy_document.asume_ecs.json
}
