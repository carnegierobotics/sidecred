# -------------------------------------------------------------------------------
# Resources
# -------------------------------------------------------------------------------
data "aws_caller_identity" "current" {}

data "aws_region" "current" {}

resource "aws_s3_bucket" "bucket" {
  bucket        = "${data.aws_caller_identity.current.account_id}-${var.name_prefix}"
  region        = data.aws_region.current.name
  acl           = "private"
  force_destroy = true

  versioning {
    enabled = true
  }

  tags = {
    environment = "dev"
    terraform   = "True"
  }
}

resource "aws_s3_bucket_object" "configurations" {
  count  = length(var.configurations)
  bucket = aws_s3_bucket.bucket.id
  key    = var.configurations[count.index].config
  source = var.configurations[count.index].config
  etag   = filemd5(var.configurations[count.index].config)
}

data "archive_file" "lambda" {
  type        = "zip"
  output_path = "${path.module}/sidecred.zip"
  source_file = var.binary_path
}

module "lambda" {
  source  = "telia-oss/lambda/aws"
  version = "3.0.0"

  name_prefix      = var.name_prefix
  filename         = "${path.module}/sidecred.zip"
  handler          = basename(var.binary_path)
  source_code_hash = data.archive_file.lambda.output_base64sha256
  policy           = data.aws_iam_policy_document.lambda.json
  environment      = merge({ AWS_REGION = data.aws_region.current.name }, var.environment)
  tags             = var.tags

}

data "aws_iam_policy_document" "lambda" {
  statement {
    effect = "Allow"

    actions = [
      "s3:GetObject", # Get config
      "s3:PutObject", # Put state
    ]

    resources = [
      "${aws_s3_bucket.bucket.arn}/*",
    ]
  }

  statement {
    effect = "Allow"

    actions = [
      "sts:AssumeRole", # STS provider
    ]

    resources = [
      "arn:aws:iam::*:role/sidecred-*",
    ]
  }

  statement {
    effect = "Allow"

    actions = [
      "logs:CreateLogGroup",
      "logs:CreateLogStream",
      "logs:PutLogEvents",
    ]

    resources = [
      "*",
    ]
  }
}

resource "aws_cloudwatch_event_rule" "main" {
  count               = length(var.configurations)
  name                = "${var.configurations[count.index].namespace}-sidecred-trigger"
  description         = "${var.configurations[count.index].namespace} sidecred trigger."
  schedule_expression = "rate(10 minute)"
  tags                = var.tags
}

resource "aws_cloudwatch_event_target" "main" {
  count = length(var.configurations)
  rule  = aws_cloudwatch_event_rule.main[count.index].name
  arn   = module.lambda.arn
  input = jsonencode(var.configurations[count.index])
}

resource "aws_lambda_permission" "main" {
  count         = length(var.configurations)
  statement_id  = "${var.configurations[count.index].namespace}-sidecred-permission"
  function_name = module.lambda.arn
  action        = "lambda:InvokeFunction"
  principal     = "events.amazonaws.com"
  source_arn    = aws_cloudwatch_event_rule.main[count.index].arn
}

