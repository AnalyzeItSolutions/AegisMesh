# Infrastructure

Four modules, wired together in `main.tf`:

| Module | What it creates |
|---|---|
| `network` | VPC over 2-3 zones, public and private subnets, NAT, S3 endpoint, flow logs, default security group closed |
| `postgres` | RDS PostgreSQL 16: encrypted, private, multi-AZ by default, master password generated and kept in Secrets Manager |
| `cluster` | EKS with encrypted secrets, control-plane logs, private API endpoint, managed arm64 node group, OIDC provider |
| `redpanda-managed` | Dedicated Redpanda Cloud cluster on AWS, its network, and the topics |

No account ids, keys or passwords are in these files. AWS credentials come from the environment or an assumed role;
Redpanda credentials from `REDPANDA_CLIENT_ID` and `REDPANDA_CLIENT_SECRET`.

```bash
cd infra/terraform
tofu init            # or terraform init
tofu plan -var region=<aws-region> -var environment=<dev|staging|prod>
```

## What has and has not been checked

- `init -backend=false`, `validate`, `fmt -check` and the static policy checks (no public database, encryption at rest, no wildcard IAM) pass.
- `plan` and `apply` have **not** been run: that needs an AWS account and a Redpanda Cloud organisation.
- TimescaleDB is not available on RDS; the telemetry history needs its own host (or Timescale Cloud). It is not created here.
- The state backend is not configured (`-backend=false` above): add an S3 backend with locking before sharing this stack.
- Redpanda private connectivity to the VPC (peering or PrivateLink) is not wired yet; `connection_type = "public"` works without it.
