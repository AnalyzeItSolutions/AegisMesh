# A dedicated Redpanda Cloud cluster on AWS and its topics. Credentials come from the REDPANDA_CLIENT_ID and REDPANDA_CLIENT_SECRET
# environment variables (a service account of the Redpanda organisation); none are written here.

terraform {
  required_providers {
    redpanda = {
      source = "redpanda-data/redpanda"
    }
  }
}

variable "name" {
  type = string
}

variable "region" {
  type = string
}

variable "zones" {
  description = "Availability zones of the cluster (three for production)."
  type        = list(string)
}

variable "throughput_tier" {
  type = string
}

variable "connection_type" {
  type    = string
  default = "private"

  validation {
    condition     = contains(["private", "public"], var.connection_type)
    error_message = "connection_type must be private or public."
  }
}

variable "network_cidr" {
  description = "Address range of the Redpanda network. Must not overlap the application VPC if the two are peered."
  type        = string
  default     = "10.50.0.0/20"
}

variable "allow_deletion" {
  description = "Permit destroying the cluster (off by default so a stray destroy cannot drop the event log)."
  type        = bool
  default     = false
}

variable "topics" {
  type = map(object({ partitions = number }))
}

variable "tags" {
  type    = map(string)
  default = {}
}

resource "redpanda_resource_group" "this" {
  name = var.name
}

resource "redpanda_network" "this" {
  name              = var.name
  resource_group_id = redpanda_resource_group.this.id
  cloud_provider    = "aws"
  region            = var.region
  cluster_type      = "dedicated"
  cidr_block        = var.network_cidr
}

resource "redpanda_cluster" "this" {
  name              = var.name
  resource_group_id = redpanda_resource_group.this.id
  network_id        = redpanda_network.this.id
  cloud_provider    = "aws"
  region            = var.region
  zones             = var.zones
  cluster_type      = "dedicated"
  connection_type   = var.connection_type
  throughput_tier   = var.throughput_tier
  allow_deletion    = var.allow_deletion
  tags              = var.tags
}

resource "redpanda_topic" "this" {
  for_each = var.topics

  name               = each.key
  partition_count    = each.value.partitions
  replication_factor = 3
  cluster_api_url    = redpanda_cluster.this.cluster_api_url
  allow_deletion     = false
}

output "cluster_id" {
  value = redpanda_cluster.this.id
}

output "cluster_api_url" {
  value = redpanda_cluster.this.cluster_api_url
}
