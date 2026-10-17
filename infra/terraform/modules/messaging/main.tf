# Domain events of an environment (T4, docs/events/README.md): one SNS topic
# per domain and one SQS queue per consumer, each with its dead-letter queue.
# A message that fails max_receive_count times moves to the DLQ, where it waits
# (14 days) for an operator instead of blocking the queue.

locals {
  subscriptions = merge([
    for q, cfg in var.queues : {
      for t in cfg.topics : "${q}.${t}" => { queue = q, topic = t, filter_policy = cfg.filter_policy }
    }
  ]...)
}

resource "aws_sns_topic" "this" {
  for_each = var.topics

  name              = "${var.name}-${each.key}"
  kms_master_key_id = "alias/aws/sns"
  tags              = var.tags
}

resource "aws_sqs_queue" "dlq" {
  for_each = var.queues

  name                      = "${var.name}-${each.key}-dlq"
  message_retention_seconds = var.dlq_retention_seconds
  sqs_managed_sse_enabled   = true
  tags                      = var.tags
}

resource "aws_sqs_queue" "this" {
  for_each = var.queues

  name                       = "${var.name}-${each.key}"
  visibility_timeout_seconds = each.value.visibility_timeout_seconds
  message_retention_seconds  = var.message_retention_seconds
  receive_wait_time_seconds  = 20 # long polling
  sqs_managed_sse_enabled    = true
  tags                       = var.tags

  redrive_policy = jsonencode({
    deadLetterTargetArn = aws_sqs_queue.dlq[each.key].arn
    maxReceiveCount     = each.value.max_receive_count
  })
}

# Only the source queue may redrive into its DLQ.
resource "aws_sqs_queue_redrive_allow_policy" "dlq" {
  for_each = var.queues

  queue_url = aws_sqs_queue.dlq[each.key].id

  redrive_allow_policy = jsonencode({
    redrivePermission = "byQueue"
    sourceQueueArns   = [aws_sqs_queue.this[each.key].arn]
  })
}

# Each queue accepts messages only from the topics it subscribes to.
data "aws_iam_policy_document" "queue" {
  for_each = { for q, cfg in var.queues : q => cfg if length(cfg.topics) > 0 }

  statement {
    sid       = "FromSubscribedTopics"
    actions   = ["sqs:SendMessage"]
    resources = [aws_sqs_queue.this[each.key].arn]

    principals {
      type        = "Service"
      identifiers = ["sns.amazonaws.com"]
    }

    condition {
      test     = "ArnEquals"
      variable = "aws:SourceArn"
      values   = [for t in each.value.topics : aws_sns_topic.this[t].arn]
    }
  }
}

resource "aws_sqs_queue_policy" "this" {
  for_each = data.aws_iam_policy_document.queue

  queue_url = aws_sqs_queue.this[each.key].id
  policy    = each.value.json
}

resource "aws_sns_topic_subscription" "this" {
  for_each = local.subscriptions

  topic_arn            = aws_sns_topic.this[each.value.topic].arn
  protocol             = "sqs"
  endpoint             = aws_sqs_queue.this[each.value.queue].arn
  raw_message_delivery = true
  filter_policy        = each.value.filter_policy

  depends_on = [aws_sqs_queue_policy.this]
}
