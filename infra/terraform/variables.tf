variable "project" {
  description = "Short name used in every resource name and tag."
  type        = string
  default     = "aegismesh"
}

variable "environment" {
  description = "Deployment environment, for example dev, staging or prod."
  type        = string

  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,14}$", var.environment))
    error_message = "environment must be 2-15 lowercase letters, digits or hyphens, starting with a letter."
  }
}

variable "region" {
  description = "AWS region for everything in this stack. No default: choose where the responders are."
  type        = string
}

variable "vpc_cidr" {
  description = "Address range of the VPC."
  type        = string
  default     = "10.40.0.0/16"
}

variable "az_count" {
  description = "How many availability zones to spread across (2 or 3)."
  type        = number
  default     = 3

  validation {
    condition     = var.az_count >= 2 && var.az_count <= 3
    error_message = "az_count must be 2 or 3."
  }
}

variable "single_nat_gateway" {
  description = "One NAT gateway for the whole VPC (cheaper, but an AZ failure cuts outbound traffic) instead of one per zone."
  type        = bool
  default     = false
}

variable "db_instance_class" {
  description = "Instance class of the PostgreSQL database."
  type        = string
  default     = "db.t4g.medium"
}

variable "db_multi_az" {
  description = "Run a standby database in a second zone."
  type        = bool
  default     = true
}

variable "cluster_admin_cidrs" {
  description = "Source ranges allowed to reach the Kubernetes API over the internet. Empty keeps the API private (reach it from inside the VPC)."
  type        = list(string)
  default     = []
}

variable "node_instance_types" {
  description = "Instance types of the Kubernetes worker nodes (arm64)."
  type        = list(string)
  default     = ["m7g.large"]
}

variable "node_desired_size" {
  type    = number
  default = 3
}

variable "node_min_size" {
  type    = number
  default = 3
}

variable "node_max_size" {
  type    = number
  default = 12
}

variable "redpanda_throughput_tier" {
  description = "Redpanda Cloud throughput tier of the dedicated cluster."
  type        = string
  default     = "tier-1-aws-v2-arm"
}

variable "redpanda_connection_type" {
  description = "private (reachable from the VPC through private networking) or public."
  type        = string
  default     = "private"
}

variable "topics" {
  description = "Topics to create on the Redpanda cluster."
  type        = map(object({ partitions = number }))
  default = {
    "telemetry.unit.locations"  = { partitions = 12 }
    "incidents.raw.ingested"    = { partitions = 6 }
    "incidents.verified.events" = { partitions = 6 }
    "logistics.routes.updated"  = { partitions = 6 }
    "audit.security.immutable"  = { partitions = 3 }
  }
}
