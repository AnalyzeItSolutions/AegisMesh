# Root of the stack: network, database, Kubernetes cluster and managed Redpanda. No account ids, keys or passwords live in these files:
# AWS credentials come from the environment or an assumed role, Redpanda credentials from REDPANDA_CLIENT_ID / REDPANDA_CLIENT_SECRET,
# and the database password is generated and kept by AWS Secrets Manager.

locals {
  name = "${var.project}-${var.environment}"
  tags = {
    Project     = var.project
    Environment = var.environment
    ManagedBy   = "terraform"
  }
}

provider "aws" {
  region = var.region

  default_tags {
    tags = local.tags
  }
}

provider "redpanda" {}

data "aws_availability_zones" "available" {
  state = "available"
}

module "network" {
  source = "./modules/network"

  name               = local.name
  cidr               = var.vpc_cidr
  azs                = slice(data.aws_availability_zones.available.names, 0, var.az_count)
  single_nat_gateway = var.single_nat_gateway
  cluster_name       = local.name
}

module "postgres" {
  source = "./modules/postgres"

  name                       = local.name
  vpc_id                     = module.network.vpc_id
  subnet_ids                 = module.network.private_subnet_ids
  allowed_security_group_ids = [module.cluster.node_security_group_id]
  instance_class             = var.db_instance_class
  multi_az                   = var.db_multi_az
}

module "cluster" {
  source = "./modules/cluster"

  name                = local.name
  vpc_id              = module.network.vpc_id
  subnet_ids          = module.network.private_subnet_ids
  public_access_cidrs = var.cluster_admin_cidrs
  node_instance_types = var.node_instance_types
  node_desired_size   = var.node_desired_size
  node_min_size       = var.node_min_size
  node_max_size       = var.node_max_size
}

module "redpanda" {
  source = "./modules/redpanda-managed"

  name            = local.name
  region          = var.region
  zones           = module.network.azs
  throughput_tier = var.redpanda_throughput_tier
  connection_type = var.redpanda_connection_type
  topics          = var.topics
  tags            = local.tags
}
