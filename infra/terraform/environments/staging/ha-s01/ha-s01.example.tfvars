# Example variables for the ephemeral HA-S01 staging window (versioned template).
#
# Copy it to ha-s01.tfvars (ignored by git: *.tfvars), adjust the values and run
#   terraform plan -var-file=ha-s01.tfvars
#
# There is NO password here on purpose: seed_user_password is sensitive and is
# only passed through the environment variable TF_VAR_seed_user_password.
# Never write it in this file or in the copy.

# Project region (Ohio); the composition rejects any other value.
region = "us-east-2"

# Date (YYYY-MM-DD) by which the window must be destroyed; becomes the TTL tag.
ttl = "2026-10-20"

# Prefix of every resource name.
name = "solventa-stg-ha-s01"

# Test users created by the administrator (D-13: permanent password, no
# invitation, no email sent). One group per user; partner_id only for partners.
# The insured user is test data only (in production: after KYC, HU-M1.1-01).
seed_users = {
  advisor    = { email = "asesor.ha-s01@example.com", group = "advisor" }
  operations = { email = "operaciones.ha-s01@example.com", group = "operations" }
  partner    = { email = "socio.ha-s01@example.com", group = "partner", partner_id = "socio-a" }
  insured    = { email = "asegurado.ha-s01@example.com", group = "insured" }
}
