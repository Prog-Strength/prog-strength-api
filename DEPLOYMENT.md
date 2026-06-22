# Deployment Guide — Prog Strength API

How the API gets to production, what's automated, and what to do when
something goes wrong.

## Architecture

- **Single EC2 instance** (`t4g.small`, Graviton/ARM64, Ubuntu 24.04) behind
  an Elastic IP, in a dedicated VPC. Provisioned by Terraform in
  [`prog-strength-infra`](https://github.com/Prog-Strength/prog-strength-infra).
- **Caddy** terminates TLS for `api.progstrength.fitness` (Let's Encrypt
  cert, auto-renewed) and reverse-proxies to the api container on the
  docker-compose internal network. The api container has no public ports.
- **SQLite** lives at
  `/home/ubuntu/prog-strength-infra/compose/api/data/app.db` on the host,
  bind-mounted into the api container.
- **semantic-release** on `prog-strength-api` cuts a new git tag on every
  `feat:` / `fix:` push to `main`, builds and pushes the image to ECR, then
  deploys via SSM Run Command (no SSH, no inbound port 22) by invoking
  `prog-strength-infra/deploy/api.sh`, which pulls the image and runs
  `docker compose pull/down/up` against that tag.
- **`deploy-caddy.yml`** in `prog-strength-infra` reloads Caddy in-place when
  only the Caddyfile changes (e.g. adding a new vhost), without bouncing
  the api.

### Host layout

```
/home/ubuntu/
└── prog-strength-infra/          # infra repo, kept on main (only host checkout)
    ├── compose/api/              # api compose project (api + caddy + litestream)
    │   ├── docker-compose.yml    # pulls the api image from ECR
    │   ├── data/                 # SQLite DB lives here (bind-mounted into api)
    │   └── .env                  # rendered by deploy/api.sh from Secrets Manager
    ├── deploy/api.sh             # runs over SSM each deploy
    └── caddy/Caddyfile           # bind-mounted into the caddy container
```

The infra repo is cloned on first boot by `modules/compute/bootstrap.sh` in
that repo; the api repo is **not** cloned on the host — the host pulls the
api image from ECR (see [Provisioning](#provisioning) below).

## Provisioning

The EC2 instance, VPC, security group, EIP, and first-boot bootstrap script
are owned by `prog-strength-infra`. To stand up a new host (or rebuild this
one), see that repo's README — the short version is:

```sh
# in prog-strength-infra/
terraform init
terraform apply -var-file=environments/prod.tfvars
```

`bootstrap.sh` (mounted as EC2 user_data) handles on first boot:

- `apt upgrade` + Docker Engine + Compose v2 install
- adds `ubuntu` to the `docker` group
- clones `prog-strength-infra` to `/home/ubuntu/prog-strength-infra`
- creates `/home/ubuntu/prog-strength-infra/compose/api/data` for SQLite

After a fresh provision, the host is ready but no containers are running
yet — the first `docker compose up` happens on the next release deploy.
To force one without pushing a `feat:` / `fix:`, run the `Manual Deploy`
workflow (`workflow_dispatch`), which deploys the latest (or a chosen) tag
over SSM without needing a release-worthy commit.

### DNS

`api.progstrength.fitness` is an A record at the registrar pointing at the
EIP. The EIP itself is stable across instance replacements (Terraform
preserves it), so DNS doesn't need to change on a host rebuild.

## Repository secrets

### `prog-strength-api` (GitHub repo settings → Secrets and variables → Actions)

App secrets no longer travel on a deploy: they are seeded into AWS Secrets
Manager (`prog-strength-backend/prod/api`) by `prog-strength-infra`'s
`seed-secrets.yml`, and the host reads them via its instance role at deploy
time. These GitHub secrets remain the source of truth and the seed source —
run `seed-secrets.yml` after rotating any of them.

| Secret                  | Purpose                                                |
| ----------------------- | ------------------------------------------------------ |
| `RELEASE_BOT_TOKEN`     | **Org-level** secret (shared across Prog-Strength repos) the release workflow uses for the `@semantic-release/git` changelog push to `main`. `main` requires status checks the fresh release commit can't have; with `enforce_admins=false` an **admin** identity bypasses them. A fine-grained PAT (resource owner `Prog-Strength`, Repository → Contents: write) or classic PAT (`repo` scope) from an owner/admin account. The default `GITHUB_TOKEN` is not an admin and is rejected (GH006). Org-secret **visibility must include this repo** — it's public, so set visibility to "All repositories" (or a selected list that includes public repos). |
| `JWT_SIGNING_KEY`       | HMAC secret for app JWTs.                              |
| `GOOGLE_CLIENT_ID`      | OAuth client ID.                                       |
| `GOOGLE_CLIENT_SECRET`  | OAuth client secret.                                   |
| `GOOGLE_REDIRECT_URL`   | OAuth callback URL (must match Google console).        |
| `DEV_AUTH`              | `true`/`false` — gates `POST /auth/dev/token`. Keep `false` in prod. |
| `CORS_ALLOWED_ORIGIN`   | Comma-separated frontend origins allowed by CORS. Each entry may use a single `*` wildcard, e.g. `https://progstrength.fitness,https://prog-strength-web-*-<vercel-scope>.vercel.app` to also allow Vercel branch previews. |
| `LITESTREAM_REPLICA_BUCKET` | S3 bucket for SQLite replicas. Output by infra repo's Terraform as `litestream_bucket_name`. |
| `LITESTREAM_REPLICA_REGION` | AWS region of the bucket. Matches `aws.region` in infra. |

### `prog-strength-infra`

| Secret                  | Purpose                                                |
| ----------------------- | ------------------------------------------------------ |
| `AWS_GHA_ROLE_ARN`      | OIDC role CI assumes (Terraform apply, ECR, SSM deploys, secret seeding). CI uses this role, not static keys. |

## Deployment flow

### On push to `prog-strength-api` `main`

`.github/workflows/release.yml`:

1. **release** job — semantic-release inspects commits since the last tag,
   bumps version, writes CHANGELOG, pushes the tag back to GitHub.
2. **build_and_push** job — builds the api image and pushes it to ECR under
   the released version tag.
3. **deploy** job (runs only if a new release was published) — assumes the
   shared OIDC role (`AWS_GHA_ROLE_ARN`) and `aws ssm send-command`s the host
   (targeted by its `Name` tag, no `EC2_HOST`) to run
   `prog-strength-infra/deploy/api.sh v<X.Y.Z>` as `ubuntu`. That script:
   - `cd /home/ubuntu/prog-strength-infra && git pull` — refreshes the infra
     checkout (compose files + Caddyfile mount target).
   - Renders `.env` from AWS Secrets Manager (read via the instance role) plus
     `APP_VERSION=v<X.Y.Z>` (which the Dockerfile embeds via `-ldflags`) —
     secrets never travel with the deploy.
   - `docker compose pull/down/up` against the released ECR image.

   The workflow polls the SSM invocation to completion and fails on any
   non-`Success` status.

Commit type matters — `chore:` / `docs:` / `refactor:` won't cut a release
and so won't deploy. Use `feat:` for minor, `fix:` for patch.

### On push to `prog-strength-infra` `main` (Caddyfile changes only)

`.github/workflows/deploy-caddy.yml` triggers only when `caddy/**` changes:

1. Via SSM Run Command (`deploy/caddy-reload.sh`), `git pull`s the infra repo
   so the bind-mount target is current.
2. `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`
   — in-place reload, preserves issued Let's Encrypt certs (in the
   `caddy_data` named volume) and live connections.

For any Terraform changes, the `apply.yml` workflow (in the infra repo)
handles them.

## Manual operations

### Break-glass shell (SSM Session Manager)

There is no inbound SSH (port 22 is closed); operators get an interactive
shell via AWS Session Manager, which dials out to AWS over 443. Requires the
Session Manager plugin for the AWS CLI.

```sh
# resolve the instance id by its Name tag, then open a session
instance_id="$(aws ec2 describe-instances --region us-east-2 \
  --filters "Name=tag:Name,Values=prog-strength-prod-backend" \
            "Name=instance-state-name,Values=running" \
  --query 'Reservations[0].Instances[0].InstanceId' --output text)"

aws ssm start-session --target "${instance_id}" --region us-east-2
```

The session lands as `ssm-user`; `sudo -u ubuntu -i` to drop into the
`ubuntu` user that owns the deploy checkouts.

### Manual deploy (skip GitHub Actions)

Re-run the `Manual Deploy` workflow (`workflow_dispatch`) and pick the tag to
deploy (blank = latest). It deploys over SSM exactly like a release, rendering
`.env` from Secrets Manager — no host login required.

### Useful commands on EC2

```sh
cd /home/ubuntu/prog-strength-infra/compose/api

docker compose ps                            # container state
docker compose logs -f api                   # follow api logs
docker compose logs -f caddy                 # follow caddy / TLS logs
docker compose restart api                   # restart api only
docker compose exec api sh                   # shell into api
docker compose exec caddy caddy reload \     # reload caddy in-place
  --config /etc/caddy/Caddyfile

du -h data/app.db                            # SQLite size
df -h                                        # disk
docker stats                                 # CPU / memory
```

## Database backups

The SQLite DB lives at
`/home/ubuntu/prog-strength-infra/compose/api/data/app.db` and is
continuously replicated to S3 by [Litestream](https://litestream.io/),
running as a docker-compose sidecar. The bucket
(`prog-strength-database-backups`) and the IAM role that grants the EC2
instance access are managed in `prog-strength-infra/modules/backup`.

How it works in compose:

- `restore` (one-shot): runs on every `docker compose up`. Pulls the
  latest snapshot from S3 *only if* `/data/app.db` doesn't exist locally
  (`-if-db-not-exists`) *and* a replica exists in S3 (`-if-replica-exists`).
  On a redeploy of an existing host this no-ops; on a fresh host (or
  manually-cleared data dir) it restores from S3.
- `api`: starts only after `restore` exits 0 (via `depends_on:
  service_completed_successfully`), so it never opens a half-restored DB.
- `litestream` (long-running): streams WAL frames + periodic snapshots
  to S3 continuously. Default 24-hour PITR window.

Authentication uses the EC2 instance profile — Litestream's AWS SDK picks
up credentials from instance metadata. No access keys live in `.env`.

### Restoring on a rebuilt host

The expected flow when the EC2 instance is replaced:

1. `terraform apply` in `prog-strength-infra` provisions the new host.
   `bootstrap.sh` clones the infra repo and creates an empty `./data/`.
2. The first deploy runs `docker compose up -d` over SSM Run Command.
3. `restore` sees no local DB + a replica in S3 → downloads the latest
   snapshot into `/data/app.db`.
4. `api` starts against the restored DB.
5. `litestream` resumes replication; from this point on, S3 has a current
   replica again.

### Manual snapshot (still works)

```sh
# on the host (open a break-glass shell first — see "Break-glass shell" above)
cd /home/ubuntu/prog-strength-infra/compose/api
cp data/app.db data/app.db.backup-$(date +%Y%m%d)
```

To pull a copy to your laptop without SSH/scp, the simplest path is the S3
Litestream replica (the canonical backup) rather than copying off the host.

### Required env vars

`LITESTREAM_REPLICA_BUCKET` and `LITESTREAM_REPLICA_REGION` must be present
in the host's `.env`. The deploy renders `.env` from the
`prog-strength-backend/prod/api` Secrets Manager container — if they're
missing, add them to the GitHub secrets and re-run `seed-secrets.yml`.

## Troubleshooting

### Deploy fails to dispatch / no SSM invocation registered

The deploy `aws ssm send-command` only reaches the host if it's a registered
SSM managed node. Confirm it shows up:

```sh
aws ssm describe-instance-information --region us-east-2 \
  --query 'InstanceInformationList[].{Id:InstanceId,Ping:PingStatus}'
```

If the host is missing, the SSM agent isn't running or the instance role
lacks `AmazonSSMManagedInstanceCore` (both handled by `prog-strength-infra` —
check the SSM console). A `send-command` that registers no invocation fails
the deploy with "No SSM invocation registered".

### Deploy succeeds but `api.progstrength.fitness` returns 502 / 503

Caddy is up but can't reach the api container. Check both containers:

```sh
docker compose ps             # api should be `Up (healthy)`
docker compose logs api       # look for migration / startup errors
```

If api is restarting in a loop, the most common cause is a missing or
malformed `.env` — re-run the `Manual Deploy` workflow (which re-renders
`.env` from Secrets Manager). If a secret itself is wrong, fix the GitHub
secret and re-run `seed-secrets.yml` before redeploying.

### `docker compose up` fails on a fresh host

The bind-mount target `/home/ubuntu/prog-strength-infra/caddy/Caddyfile`
doesn't exist. Either bootstrap didn't run (check
`/var/log/cloud-init-output.log`) or the infra repo clone is missing.
Fix:

```sh
git clone https://github.com/Prog-Strength/prog-strength-infra.git \
  /home/ubuntu/prog-strength-infra
```

### Caddy can't issue a certificate

Hit Let's Encrypt's rate limit during testing? Check:

```sh
docker compose logs caddy | grep -i "acme\|rate"
```

The `caddy_data` named volume holds issued certs and the ACME account key
— do **not** wipe it. If you do, you'll need to wait for the rate limit
to clear (up to 7 days) or use a staging endpoint.

### Out of disk

```sh
docker system prune -a       # drop unused images
du -h data/app.db            # check DB size
```

### Migrations didn't run

```sh
docker compose logs api | grep -i migration
```

## Cost (rough)

- `t4g.small` (Graviton, on-demand, us-east-2): ~$12/mo
- 8 GiB gp3 root volume: ~$0.65/mo
- Elastic IP (attached): free
- Data transfer out: free below 100 GiB/mo on the new AWS free tier

Total: roughly **$13/mo** under typical load.

## Next steps

- **Uptime monitoring** for `https://api.progstrength.fitness/health`.
- **Add the MCP server vhost** to `prog-strength-infra/caddy/Caddyfile`
  once that service ships — `deploy-caddy.yml` will roll it out without
  bouncing the api.
