# Variables of the HA-D02 staging window (versioned template, decisions D-6 and
# D-7 of the AWS phase). Use it as it is:
#
#   terraform plan -out p1.tfplan -var-file=ha-d02.example.tfvars -var ttl=<YYYY-MM-DD> \
#     -var image_tag=<sha of 12> -var 'image_tag_overrides={...}' -var services_enabled=false
#
# There is NO password and NO image tag here on purpose: seed_user_password is
# sensitive and only travels in TF_VAR_seed_user_password (never in a file), and
# the tags go on the command line. The users are fictitious (@example.com).

# Test users of the user pool, one per role of the smoke test: the insured
# accepts and pays, the advisor decides the inbox, operations resumes a charge.
seed_users = {
  insured    = { email = "asegurado.ha-d02@example.com", group = "insured" }
  advisor    = { email = "asesor.ha-d02@example.com", group = "advisor" }
  operations = { email = "operaciones.ha-d02@example.com", group = "operations" }
}

# Retry window of 5 minutes instead of 24 hours, so the window sees the issuance
# exceptions (CA-3), and a longest wait of 15 s between charge attempts instead
# of 60 s, so the restored gateway is reached in time (D-6). Only in staging;
# the defaults of the service do not change.
origination_environment = {
  RETRY_WINDOW_SECONDS = "300"
  CHARGE_RETRY_MAX_MS  = "15000"
}

# Cost of the window filtered by owner and story (D-7).
extra_tags = {
  Owner = "nicolas"
  Story = "HA-D02"
}
