#!/usr/bin/env bash
# Publishes an automatic parametric payment to the claims topic of a cloud
# environment (staging or production-demo), as the parametric engine of claims
# will publish it (HU-M3.1-01, docs/events/README.md). The engine is not built
# yet, so this is how a payment reaches the notification center of the app in
# AWS: notifications consumes it from its queue, records it and sends the push
# to the push stub.
#
# Usage (from the repository root, with AWS credentials allowed to publish to
# the topic):
#   SUBJECT=<sub of the insured> bash tests/e2e/hu-m3.1/nube.sh [FLIGHT] [MINUTES]
#       # default: the payment of the prototype, AV-8421 215 min late, $ 180.000 COP
#
# Options (environment):
#   PLATFORM_NAME   name of the platform, the prefix of its topics (default solventa-stg)
#   AWS_REGION      default us-east-2
#
# SUBJECT is the sub of the insured that signs in on the phone, read with the
# read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# Then open the notification center on the phone (or wait for its next check):
# the payment is there, and the push stub logs it as "message accepted" in the
# CloudWatch log group of the push service.
set -euo pipefail

command -v aws >/dev/null || {
  echo "aws is required" >&2
  exit 1
}
if [[ -z "${SUBJECT:-}" ]]; then
  echo "Set SUBJECT with the sub of the insured (aws cognito-idp admin-get-user ..., read-only)." >&2
  exit 2
fi

REGION="${AWS_REGION:-us-east-2}"
NAME="${PLATFORM_NAME:-solventa-stg}"
FLIGHT="${1:-AV-8421}"
MINUTES="${2:-215}"
ACCOUNT="$(MSYS_NO_PATHCONV=1 aws sts get-caller-identity --query Account --output text | tr -d '\r')"
TOPIC="arn:aws:sns:$REGION:$ACCOUNT:$NAME-claims"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
PAYOUT="ppo-$(echo "$FLIGHT" | tr -d '-' | tr '[:upper:]' '[:lower:]')-$(date -u +%Y%m%d%H%M%S)"

MSYS_NO_PATHCONV=1 AWS_PAGER="" aws sns publish --region "$REGION" --topic-arn "$TOPIC" \
  --message-attributes 'event_type={DataType=String,StringValue=ParametricPayoutCompleted}' \
  --message "{\"event_id\":\"parametric-payout-completed-$PAYOUT\",\"event_type\":\"ParametricPayoutCompleted\",\"event_version\":1,
    \"source\":\"claims\",\"occurred_at\":\"$NOW\",\"payload\":{\"payout_id\":\"$PAYOUT\",\"claim_id\":\"SIN-2026-01702\",
    \"policy_id\":\"VJ-2026-000931\",\"subject\":\"$SUBJECT\",\"amount_minor\":18000000,\"currency\":\"COP\",
    \"trigger\":{\"parameter\":\"flight_delay\",\"event_reference\":\"$FLIGHT\",\"threshold\":180,\"observed\":$MINUTES,
    \"unit\":\"minutes\",\"observed_at\":\"$NOW\"},\"payment_method\":{\"brand\":\"visa\",\"last4\":\"1111\"},
    \"idempotency_key\":\"pay-$PAYOUT\",\"paid_at\":\"$NOW\"}}" >/dev/null

echo "== Published the automatic payment $PAYOUT ($FLIGHT, $MINUTES min, \$ 180.000 COP) to $NAME-claims for $SUBJECT"
