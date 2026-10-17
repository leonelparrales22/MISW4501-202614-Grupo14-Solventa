# Secrets Manager secrets of an environment (T4), for example the
# DATABASE_URL of each service. The names come apart from the values because a
# sensitive value cannot drive for_each. The values do reach the Terraform
# state, which lives in the encrypted, private state bucket (T4).

resource "aws_secretsmanager_secret" "this" {
  for_each = var.secrets

  name                    = "${var.prefix}/${each.key}"
  description             = each.value
  recovery_window_in_days = var.recovery_window_in_days
  tags                    = var.tags
}

resource "aws_secretsmanager_secret_version" "this" {
  for_each = var.secrets

  secret_id     = aws_secretsmanager_secret.this[each.key].id
  secret_string = var.values[each.key]
}
