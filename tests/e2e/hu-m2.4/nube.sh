#!/usr/bin/env bash
# Publishes the delay of a flight of the travel policy of an insured to the
# iot topic of a cloud environment (staging or production-demo), as the
# telemetry will publish it (ParametricTriggerObserved, docs/events/README.md).
# The parametric engine of claims consumes it from its queue, files the
# claim, resolves it automatically, pays it to the payment gateway stub and
# publishes ParametricPayoutCompleted, which notifications turns into the
# notification and the push of HU-M3.1-01 (HU-M2.4-01, criterion 3).
#
# Usage (from the repository root, Git Bash, with AWS credentials allowed to
# publish to the topic):
#   SUBJECT=<sub of the insured> bash tests/e2e/hu-m2.4/nube.sh [FLIGHT] [MINUTES]
#       # default: the flight of the prototype, AV-8421 215 min late (threshold 180)
#
# Options (environment):
#   POLICY_ID       the travel policy of the insured (default: the one the
#                   policy core stub derives from SUBJECT, computed here the
#                   same way, stubs/policy-core: FNV-32a of the holder id)
#   PLATFORM_NAME   name of the platform, the prefix of its topics (default solventa-stg)
#   AWS_REGION      default us-east-2
#
# SUBJECT is the sub of the insured that signs in on the phone, read with the
# read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# Then open "Mis siniestros" on the phone: the claim is there, resolved
# automatically, with the flight, the threshold and the minutes of the delay.
# Its trace is in the CloudWatch log group of claims ("parametric claim
# resolved automatically", "indemnity paid") and of the payment gateway.
# This replaces tests/e2e/hu-m3.1/nube.sh, which published the payment
# directly and left no claim behind.
set -euo pipefail

for tool in aws python; do
  command -v "$tool" >/dev/null || {
    echo "$tool is required" >&2
    exit 1
  }
done
if [[ -z "${SUBJECT:-}" ]]; then
  echo "Set SUBJECT with the sub of the insured (aws cognito-idp admin-get-user ..., read-only)." >&2
  exit 2
fi

REGION="${AWS_REGION:-us-east-2}"
NAME="${PLATFORM_NAME:-solventa-stg}"
FLIGHT="${1:-AV-8421}"
MINUTES="${2:-215}"
# The travel policy the policy core stub gives the holder: it starts 30 to 329
# days before today (UTC) and its number comes from the same hash.
POLICY="${POLICY_ID:-$(python -c "
import datetime, sys
h = 0x811c9dc5
for b in sys.argv[1].encode():
    h = ((h ^ b) * 0x01000193) & 0xffffffff
start = datetime.datetime.now(datetime.timezone.utc).date() - datetime.timedelta(days=30 + h % 300)
print('VJ-%d-%06d' % (start.year, (h // 11) % 1000000))" "$SUBJECT")}"
ACCOUNT="$(MSYS_NO_PATHCONV=1 aws sts get-caller-identity --query Account --output text | tr -d '\r')"
TOPIC="arn:aws:sns:$REGION:$ACCOUNT:$NAME-iot"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
OBS="obs-$(echo "$FLIGHT" | tr -d '-' | tr '[:upper:]' '[:lower:]')-$(date -u +%Y%m%d%H%M%S)"

MSYS_NO_PATHCONV=1 AWS_PAGER="" aws sns publish --region "$REGION" --topic-arn "$TOPIC" \
  --message-attributes 'event_type={DataType=String,StringValue=ParametricTriggerObserved}' \
  --message "{\"event_id\":\"parametric-trigger-observed-$OBS\",\"event_type\":\"ParametricTriggerObserved\",
    \"event_version\":1,\"source\":\"iot\",\"occurred_at\":\"$NOW\",\"payload\":{\"observation_id\":\"$OBS\",
    \"subject\":\"$SUBJECT\",\"policy_id\":\"$POLICY\",\"parameter\":\"flight_delay\",\"event_reference\":\"$FLIGHT\",
    \"observed\":$MINUTES,\"unit\":\"minutes\",\"observed_at\":\"$NOW\"}}" >/dev/null

echo "== Published the observation $OBS to $NAME-iot: flight $FLIGHT $MINUTES min late, policy $POLICY of $SUBJECT"
echo "   With 180 min or more, claims resolves and pays it in seconds; open \"Mis siniestros\" on the phone."
