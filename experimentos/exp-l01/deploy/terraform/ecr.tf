# Un solo repositorio con dos tags: "app" para el binario Go (orquestador y
# fuentes) y "k6" para el generador de carga con el guion adentro.

resource "aws_ecr_repository" "exp" {
  name                 = local.nombre
  image_tag_mutability = "MUTABLE"
  force_delete         = true # que el destroy no se atasque por imágenes adentro

  image_scanning_configuration {
    scan_on_push = false
  }
}
