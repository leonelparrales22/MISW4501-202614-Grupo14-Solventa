output "address" {
  description = "Host name of the instance."
  value       = aws_db_instance.this.address
}

output "port" {
  description = "PostgreSQL port."
  value       = aws_db_instance.this.port
}

output "master_user_secret_arn" {
  description = "Secret (managed by RDS) with the administrator username and password; only the database bootstrap task reads it."
  value       = aws_db_instance.this.master_user_secret[0].secret_arn
}

output "security_group_id" {
  description = "Security group of the instance."
  value       = aws_security_group.this.id
}

output "databases" {
  description = "Database and login role of each service."
  value       = { for d in var.databases : d => { database = d, username = "${d}_app" } }
}

output "passwords" {
  description = "Password of the login role of each service."
  value       = { for d, p in random_password.database : d => p.result }
  sensitive   = true
}
