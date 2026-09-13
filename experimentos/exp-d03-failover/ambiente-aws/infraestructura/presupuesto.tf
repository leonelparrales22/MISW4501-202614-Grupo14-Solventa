# Red de seguridad de costo: presupuesto mensual con alertas por correo.

resource "aws_budgets_budget" "experimento" {
  count = var.presupuesto_correo != "" ? 1 : 0

  name         = "${var.prefijo}-presupuesto"
  budget_type  = "COST"
  limit_amount = tostring(var.presupuesto_limite_usd)
  limit_unit   = "USD"
  time_unit    = "MONTHLY"

  # Se mide el uso bruto, antes de aplicar creditos: con creditos incluidos el
  # costo neto seria 0 y las alertas nunca se dispararian.
  cost_types {
    include_credit = false
    include_refund = false
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 25
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.presupuesto_correo]
  }

  notification {
    comparison_operator        = "GREATER_THAN"
    threshold                  = 75
    threshold_type             = "PERCENTAGE"
    notification_type          = "ACTUAL"
    subscriber_email_addresses = [var.presupuesto_correo]
  }
}
