# Capa de datos: RDS PostgreSQL Multi-AZ (primaria en una AZ, standby síncrona
# en la otra) y la credencial en Secrets Manager, como en el modelo de despliegue.

resource "random_password" "db" {
  length  = 32
  special = false
}

resource "aws_secretsmanager_secret" "db" {
  name                    = "${var.prefijo}/originacion/db-password"
  description             = "Contrasena de la base del stub de originacion (EXP-D03)"
  recovery_window_in_days = 0
}

resource "aws_secretsmanager_secret_version" "db" {
  secret_id     = aws_secretsmanager_secret.db.id
  secret_string = random_password.db.result
}

resource "aws_db_subnet_group" "principal" {
  name       = "${var.prefijo}-db-subredes"
  subnet_ids = aws_subnet.publica[*].id
}

resource "aws_db_instance" "originacion" {
  identifier                   = "${var.prefijo}-originacion"
  engine                       = "postgres"
  engine_version               = var.db_version
  instance_class               = var.db_clase
  allocated_storage            = 20
  storage_type                 = "gp3"
  db_name                      = "originacion"
  username                     = "solventa"
  password                     = random_password.db.result
  multi_az                     = true
  db_subnet_group_name         = aws_db_subnet_group.principal.name
  vpc_security_group_ids       = [aws_security_group.rds.id]
  publicly_accessible          = false
  backup_retention_period      = 0
  skip_final_snapshot          = true
  deletion_protection          = false
  apply_immediately            = true
  auto_minor_version_upgrade   = false
  performance_insights_enabled = false

  tags = { Name = "${var.prefijo}-originacion" }
}
