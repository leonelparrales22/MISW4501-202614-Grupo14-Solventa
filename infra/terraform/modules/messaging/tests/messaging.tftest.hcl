# Configuration tests of the SNS topics and SQS queues (T4). They run with a
# mocked AWS provider: no credentials, no network, nothing is created.
#
#   terraform init -backend=false && terraform test

mock_provider "aws" {
  mock_data "aws_iam_policy_document" {
    defaults = {
      json = "{}"
    }
  }
}

variables {
  name   = "solventa-test"
  topics = ["origination", "claims"]
  queues = {
    audit         = { topics = ["origination", "claims"] }
    notifications = { topics = ["origination", "claims"] }
    origination   = { topics = ["origination"], max_receive_count = 3 }
  }
}

run "every_queue_has_its_dead_letter_queue" {
  command = plan

  assert {
    condition     = length(aws_sqs_queue.this) == 3 && length(aws_sqs_queue.dlq) == 3
    error_message = "Every consumer queue needs its own DLQ."
  }

  assert {
    condition     = aws_sqs_queue.dlq["audit"].name == "solventa-test-audit-dlq" && aws_sqs_queue.dlq["audit"].message_retention_seconds == 1209600
    error_message = "DLQs are named <queue>-dlq and keep messages 14 days."
  }

  assert {
    condition     = alltrue([for q in aws_sqs_queue.this : q.sqs_managed_sse_enabled]) && alltrue([for q in aws_sqs_queue.dlq : q.sqs_managed_sse_enabled])
    error_message = "Every queue must be encrypted (SSE-SQS)."
  }
}

run "one_subscription_per_queue_and_topic" {
  command = plan

  assert {
    condition     = length(aws_sns_topic_subscription.this) == 5
    error_message = "audit and notifications subscribe to both topics, origination only to its own."
  }

  assert {
    condition     = alltrue([for s in aws_sns_topic_subscription.this : s.protocol == "sqs" && s.raw_message_delivery])
    error_message = "Subscriptions deliver the raw message to SQS."
  }

  assert {
    condition     = length(aws_sns_topic.this) == 2 && aws_sns_topic.this["claims"].name == "solventa-test-claims"
    error_message = "One topic per domain, named <prefix>-<domain>."
  }
}

run "rejects_an_invalid_max_receive_count" {
  command = plan

  variables {
    queues = { audit = { topics = ["origination"], max_receive_count = 0 } }
  }

  expect_failures = [var.queues]
}
