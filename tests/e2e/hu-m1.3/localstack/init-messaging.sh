#!/usr/bin/env bash
# Ready hook of LocalStack (HU-M1.3-01): the messaging of the platform as
# infra/terraform/modules/messaging creates it, with local names: the topics
# origination and claims, the audit queue with its dead-letter queue (5
# receives, 30 s of visibility, 4 days of retention; 14 days in the DLQ) and
# the subscription of the queue to both topics with raw message delivery.
# It runs at every start, so a restarted LocalStack gets the same resources.
set -euo pipefail

R="--region us-east-2"
PREFIX="solventa-local"
ACCOUNT="000000000000"

for topic in origination claims; do
  awslocal sns create-topic $R --name "$PREFIX-$topic" >/dev/null
done

awslocal sqs create-queue $R --queue-name "$PREFIX-audit-dlq" \
  --attributes MessageRetentionPeriod=1209600 >/dev/null
DLQ_ARN="arn:aws:sqs:us-east-2:$ACCOUNT:$PREFIX-audit-dlq"
awslocal sqs create-queue $R --queue-name "$PREFIX-audit" \
  --attributes "{\"VisibilityTimeout\":\"30\",\"MessageRetentionPeriod\":\"345600\",\"RedrivePolicy\":\"{\\\"deadLetterTargetArn\\\":\\\"$DLQ_ARN\\\",\\\"maxReceiveCount\\\":\\\"5\\\"}\"}" >/dev/null

for topic in origination claims; do
  awslocal sns subscribe $R --topic-arn "arn:aws:sns:us-east-2:$ACCOUNT:$PREFIX-$topic" --protocol sqs \
    --notification-endpoint "arn:aws:sqs:us-east-2:$ACCOUNT:$PREFIX-audit" \
    --attributes RawMessageDelivery=true >/dev/null
done

echo "hu-m1.3 messaging ready"
