#!/usr/bin/env bash
# Publishes the expiry alert of the auto policy of an insured to the
# origination topic of a cloud environment (staging or production-demo), as
# the sweep of origination publishes it (PolicyExpiryApproaching,
# docs/events/README.md). notifications consumes it from its queue, records
# the notification of the center and sends the push through Firebase Cloud
# Messaging (HU-M3.2-01, criteria 1, 2 and 4).
#
# Why publish it by hand: the policy core stub deployed in the cloud has no
# seed, and the validity it derives for each holder is anchored to today, so
# its policies never get 30, 15 or 5 days from their end and the sweep never
# alerts there. The sweep itself (milestones, no repetition, renewed periods:
# criteria 2 and 3) is covered by the tests of origination and by
# tests/e2e/hu-m3.2/app-local.sh.
#
# Usage (from the repository root, Git Bash, with AWS credentials allowed to
# publish to the topic):
#   SUBJECT=<sub of the insured> bash tests/e2e/hu-m3.2/nube.sh [DAYS_LEFT]
#       # default 15: the 15-day alert; 30 to 16 give the 30-day one, 14 to 6
#       # the 15-day one and 5 to 0 the 5-day one ("vence hoy" with 0)
#
# Options (environment):
#   POLICY_ID       the policy of the alert (default: the active auto policy the
#                   policy core stub derives from SUBJECT, computed here the same
#                   way, stubs/policy-core: FNV-32a of the holder id), so "Ver
#                   póliza" opens a policy of the wallet of the insured
#   PLATFORM_NAME   name of the platform, the prefix of its topics (default solventa-stg)
#   AWS_REGION      default us-east-2
#
# The alert says the policy ends at 23:59:59 in Colombia DAYS_LEFT days from
# today; the wallet keeps showing the real validity of the stub. Publishing
# the same DAYS_LEFT again on the same day repeats the event id, and
# notifications keeps the notification it already has (criterion 2).
#
# SUBJECT is the sub of the insured that signs in on the phone, read with the
# read-only
#   aws cognito-idp admin-get-user --user-pool-id us-east-2_EWwg8NL2C --username asegurado@example.com
# Its trace is in the CloudWatch log group of notifications
# ("policy expiry notification recorded").
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

DAYS="${1:-15}"
if ! [[ "$DAYS" =~ ^[0-9]+$ ]] || ((DAYS > 30)); then
  echo "DAYS_LEFT must be a whole number from 0 to 30 (more than 30 days has no alert)." >&2
  exit 2
fi
# The last milestone crossed: the smallest of 30, 15 and 5 not below the days left.
if ((DAYS <= 5)); then
  MILESTONE=5
elif ((DAYS <= 15)); then
  MILESTONE=15
else
  MILESTONE=30
fi

REGION="${AWS_REGION:-us-east-2}"
NAME="${PLATFORM_NAME:-solventa-stg}"
# The active auto policy the policy core stub gives the holder, with its plate:
# it starts 30 to 329 days before today (UTC) and its number comes from the
# same hash.
read -r DERIVED PLATE < <(python -c "
import datetime, sys
h = 0x811c9dc5
for b in sys.argv[1].encode():
    h = ((h ^ b) * 0x01000193) & 0xffffffff
start = datetime.datetime.now(datetime.timezone.utc).date() - datetime.timedelta(days=30 + h % 300)
letters = ''.join(chr(ord('A') + x % 26) for x in (h, h // 26, h // 676))
print('AU-%d-%06d %s-%03d' % (start.year, h % 1000000, letters, h % 1000))" "$SUBJECT" | tr -d '\r')
POLICY="${POLICY_ID:-$DERIVED}"
# The end of the period: 23:59:59 in Colombia (UTC-5), DAYS days from today there.
read -r END END_DAY < <(python -c "
import datetime, sys
cot = datetime.timezone(datetime.timedelta(hours=-5))
day = datetime.datetime.now(cot).date() + datetime.timedelta(days=int(sys.argv[1]))
end = datetime.datetime(day.year, day.month, day.day, 23, 59, 59, tzinfo=cot).astimezone(datetime.timezone.utc)
print(end.strftime('%Y-%m-%dT%H:%M:%SZ'), day.isoformat())" "$DAYS" | tr -d '\r')
ACCOUNT="$(MSYS_NO_PATHCONV=1 aws sts get-caller-identity --query Account --output text | tr -d '\r')"
TOPIC="arn:aws:sns:$REGION:$ACCOUNT:$NAME-origination"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
EVENT="policy-expiry-approaching-$POLICY-$END_DAY-$MILESTONE"

MSYS_NO_PATHCONV=1 AWS_PAGER="" aws sns publish --region "$REGION" --topic-arn "$TOPIC" \
  --message-attributes 'event_type={DataType=String,StringValue=PolicyExpiryApproaching}' \
  --message "{\"event_id\":\"$EVENT\",\"event_type\":\"PolicyExpiryApproaching\",\"event_version\":1,
    \"source\":\"origination\",\"occurred_at\":\"$NOW\",\"payload\":{\"policy_id\":\"$POLICY\",
    \"subject\":\"$SUBJECT\",\"source\":\"policy_core\",\"line\":\"auto\",\"product\":\"Autos\",
    \"insured_object\":\"$PLATE\",\"coverage_end\":\"$END\",\"milestone_days\":$MILESTONE,
    \"days_left\":$DAYS,\"detected_at\":\"$NOW\"}}" >/dev/null

echo "== Published $EVENT to $NAME-origination"
echo "   Policy $POLICY ($PLATE) of $SUBJECT ends on $END_DAY in Colombia: $DAYS days left, milestone of $MILESTONE days."
echo "   In seconds the push arrives on the phone (Firebase) and the alert is in the notification center."
