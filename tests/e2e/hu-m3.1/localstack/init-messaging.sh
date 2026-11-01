#!/usr/bin/env bash
# Ready hook of LocalStack (HU-M3.1-01): the claims topic and the queue of the
# notifications service with its dead-letter queue (5 receives, 30 s of
# visibility), subscribed to the topic with raw message delivery, as
# infra/terraform/modules/messaging creates them. It runs at every start.
set -euo pipefail

R="--region us-east-2"
PREFIX="solventa-local"
ACCOUNT="000000000000"

awslocal sns create-topic $R --name "$PREFIX-claims" >/dev/null
awslocal sqs create-queue $R --queue-name "$PREFIX-notifications-dlq" \
  --attributes MessageRetentionPeriod=1209600 >/dev/null
DLQ_ARN="arn:aws:sqs:us-east-2:$ACCOUNT:$PREFIX-notifications-dlq"
awslocal sqs create-queue $R --queue-name "$PREFIX-notifications" \
  --attributes "{\"VisibilityTimeout\":\"30\",\"MessageRetentionPeriod\":\"345600\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"$DLQ_ARN\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}" >/dev/null
awslocal sns subscribe $R --topic-arn "arn:aws:sns:us-east-2:$ACCOUNT:$PREFIX-claims" --protocol sqs \
  --notification-endpoint "arn:aws:sqs:us-east-2:$ACCOUNT:$PREFIX-notifications" \
  --attributes RawMessageDelivery=true >/dev/null

echo "hu-m3.1 messaging ready"
