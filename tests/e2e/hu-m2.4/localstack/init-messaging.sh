#!/usr/bin/env bash
# Ready hook of LocalStack (HU-M2.4-01), as infra/terraform/modules/messaging
# creates them: the iot topic of the external events and the claims queue with
# its dead-letter queue (5 receives, 30 s of visibility), subscribed to it with
# raw message delivery; and the claims topic, where the outbox of claims
# publishes ParametricPayoutCompleted, with a queue of its own
# (solventa-local-claims-events) so app-local.sh can show what was published,
# in place of the queue of notifications. It runs at every start.
set -euo pipefail

R="--region us-east-2"
PREFIX="solventa-local"
ACCOUNT="000000000000"

queue_with_dlq() {
  local name="$1"
  awslocal sqs create-queue $R --queue-name "$name-dlq" --attributes MessageRetentionPeriod=1209600 >/dev/null
  local dlq="arn:aws:sqs:us-east-2:$ACCOUNT:$name-dlq"
  awslocal sqs create-queue $R --queue-name "$name" \
    --attributes "{\"VisibilityTimeout\":\"30\",\"MessageRetentionPeriod\":\"345600\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"$dlq\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}" >/dev/null
}

subscribe() {
  awslocal sns subscribe $R --topic-arn "arn:aws:sns:us-east-2:$ACCOUNT:$PREFIX-$1" --protocol sqs \
    --notification-endpoint "arn:aws:sqs:us-east-2:$ACCOUNT:$2" --attributes RawMessageDelivery=true >/dev/null
}

awslocal sns create-topic $R --name "$PREFIX-iot" >/dev/null
awslocal sns create-topic $R --name "$PREFIX-claims" >/dev/null
queue_with_dlq "$PREFIX-claims"
queue_with_dlq "$PREFIX-claims-events"
subscribe iot "$PREFIX-claims"
subscribe claims "$PREFIX-claims-events"

echo "hu-m2.4 messaging ready"
