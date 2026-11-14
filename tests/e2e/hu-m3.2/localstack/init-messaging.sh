#!/usr/bin/env bash
# Ready hook of LocalStack (HU-M3.2-01), as infra/terraform/modules/messaging
# creates them: the origination topic, where the outbox of origination
# publishes PolicyExpiryApproaching, and the queue of notifications with its
# dead-letter queue (5 receives, 30 s of visibility), subscribed to it with raw
# message delivery. It runs at every start.
set -euo pipefail

R="--region us-east-2"
PREFIX="solventa-local"
ACCOUNT="000000000000"

awslocal sns create-topic $R --name "$PREFIX-origination" >/dev/null
awslocal sqs create-queue $R --queue-name "$PREFIX-notifications-dlq" --attributes MessageRetentionPeriod=1209600 >/dev/null
DLQ_ARN="arn:aws:sqs:us-east-2:$ACCOUNT:$PREFIX-notifications-dlq"
awslocal sqs create-queue $R --queue-name "$PREFIX-notifications" \
  --attributes "{\"VisibilityTimeout\":\"30\",\"MessageRetentionPeriod\":\"345600\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"$DLQ_ARN\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}" >/dev/null
awslocal sns subscribe $R --topic-arn "arn:aws:sns:us-east-2:$ACCOUNT:$PREFIX-origination" --protocol sqs \
  --notification-endpoint "arn:aws:sqs:us-east-2:$ACCOUNT:$PREFIX-notifications" --attributes RawMessageDelivery=true >/dev/null

echo "hu-m3.2 messaging ready"
