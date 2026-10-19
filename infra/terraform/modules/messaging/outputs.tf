output "topic_arns" {
  description = "ARN of each SNS topic."
  value       = { for k, t in aws_sns_topic.this : k => t.arn }
}

output "queue_urls" {
  description = "URL of each consumer queue."
  value       = { for k, q in aws_sqs_queue.this : k => q.id }
}

output "queue_arns" {
  description = "ARN of each consumer queue."
  value       = { for k, q in aws_sqs_queue.this : k => q.arn }
}

output "dlq_urls" {
  description = "URL of the dead-letter queue of each consumer."
  value       = { for k, q in aws_sqs_queue.dlq : k => q.id }
}
