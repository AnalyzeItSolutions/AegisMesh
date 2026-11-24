output "vpc_id" {
  value = module.network.vpc_id
}

output "private_subnet_ids" {
  value = module.network.private_subnet_ids
}

output "database_endpoint" {
  value = module.postgres.endpoint
}

output "database_credentials_secret_arn" {
  description = "Secrets Manager secret holding the database master credentials (never written to state or to these files)."
  value       = module.postgres.master_user_secret_arn
}

output "cluster_name" {
  value = module.cluster.cluster_name
}

output "cluster_endpoint" {
  value = module.cluster.endpoint
}

output "redpanda_cluster_api_url" {
  value = module.redpanda.cluster_api_url
}
