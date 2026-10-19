# Configuration tests of the PostgreSQL instance (T4). They run with mocked
# providers: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {}
mock_provider "random" {}

variables {
  name                       = "solventa-test"
  vpc_id                     = "vpc-12345678"
  subnet_ids                 = ["subnet-aaaa1111", "subnet-bbbb2222"]
  allowed_security_group_ids = { tasks = "sg-tasks111" }
  databases                  = ["origination", "claims", "partners", "audit", "notifications"]
}

run "one_private_encrypted_instance_single_az_by_default" {
  command = plan

  assert {
    condition     = aws_db_instance.this.multi_az == false
    error_message = "multi_az must be false by default (cost); it is enabled for the demo."
  }

  assert {
    condition     = aws_db_instance.this.publicly_accessible == false && aws_db_instance.this.storage_encrypted
    error_message = "The instance must be private and encrypted."
  }

  assert {
    condition     = aws_db_instance.this.manage_master_user_password == true && aws_db_instance.this.password == null
    error_message = "The administrator password must be managed by RDS in Secrets Manager, never set by Terraform."
  }

  assert {
    condition     = aws_db_instance.this.engine == "postgres" && aws_db_instance.this.instance_class == "db.t4g.micro"
    error_message = "PostgreSQL on db.t4g.micro by default."
  }
}

run "multi_az_is_a_variable" {
  command = plan

  variables {
    multi_az = true
  }

  assert {
    condition     = aws_db_instance.this.multi_az == true && aws_db_instance.this.apply_immediately == true
    error_message = "multi_az = true must enable the standby and apply it now."
  }
}

run "one_role_and_password_per_service" {
  command = plan

  assert {
    condition     = length(random_password.database) == 5 && alltrue([for p in random_password.database : p.special == false && p.length == 32])
    error_message = "Each service needs its own 32-character URL-safe password."
  }

  assert {
    condition     = output.databases["audit"].username == "audit_app" && output.databases["audit"].database == "audit"
    error_message = "Each service owns the database <service> with the login role <service>_app."
  }
}

run "postgres_only_from_the_tasks" {
  command = plan

  assert {
    condition     = length(aws_vpc_security_group_ingress_rule.postgres) == 1 && aws_vpc_security_group_ingress_rule.postgres["tasks"].from_port == 5432
    error_message = "Only the task security group may reach port 5432."
  }
}

run "final_snapshot_unless_skipped" {
  command = plan

  assert {
    condition     = aws_db_instance.this.final_snapshot_identifier == "solventa-test-final" && aws_db_instance.this.deletion_protection
    error_message = "By default destroy is protected and takes a final snapshot."
  }
}

run "rejects_invalid_database_names" {
  command = plan

  variables {
    databases = ["Origination-DB"]
  }

  expect_failures = [var.databases]
}
