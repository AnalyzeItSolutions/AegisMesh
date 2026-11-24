terraform {
  required_version = ">= 1.5.0"

  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.80"
    }
    redpanda = {
      source  = "redpanda-data/redpanda"
      version = "~> 2.7"
    }
  }
}
