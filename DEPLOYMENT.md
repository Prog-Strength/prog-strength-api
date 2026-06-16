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
- **SQLite** lives at `/home/ubuntu/prog-strength-api/data/app.db` on the
  host, bind-mounted into the api container.
- **semantic-release** on `prog-strength-api` cuts a new git tag on every
  `feat:` / `fix:` push to `main`, then SSHes into the EC2 host and runs
  `docker compose up --build -d` against that tag.
- **`deploy-caddy.yml`** in `prog-strength-infra` reloads Caddy in-place when
  only the Caddyfile changes (e.g. adding a new vhost), without bouncing
  the api.

### Host layout

```
/home/ubuntu/
├── prog-strength-api/        # api repo, deployed at the released tag
│   ├── docker-compose.yml    # mounts the Caddyfile from the infra clone
│   ├── data/                 # SQLite DB lives here (bind-mounted into api)
│   └── .env                  # written by release.yml each deploy
└── prog-strength-infra/      # infra repo, kept on main
    └── caddy/Caddyfile       # bind-mounted into the caddy container
```

Both repos are cloned on first boot by `modules/compute/bootstrap.sh` in the
infra repo (see [Provisioning](#provisioning) below).

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
- clones `prog-strength-api` to `/home/ubuntu/prog-strength-api`
- clones `prog-strength-infra` to `/home/ubuntu/prog-strength-infra`
- creates `/home/ubuntu/prog-strength-api/data` for SQLite

After a fresh provision, the host is ready but no containers are running
yet — the first `docker compose up` happens on the next release deploy.
To force one without pushing a `feat:` / `fix:`, manually run the
`Release and Deploy` workflow via `workflow_dispatch`; if semantic-release
finds no release-worthy commits it'll no-op, in which case SSH in and run
`docker compose up --build -d` manually against the latest tag.

### DNS

`api.progstrength.fitness` is an A record at the registrar pointing at the
EIP. The EIP itself is stable across instance replacements (Terraform
preserves it), so DNS doesn't need to change on a host rebuild.

## Repository secrets

### `prog-strength-api` (GitHub repo settings → Secrets and variables → Actions)

| Secret                  | Purpose                                                |
| ----------------------- | ------------------------------------------------------ |
| `EC2_HOST`              | Elastic IP of the prod instance (not the domain).      |
| `EC2_SSH_KEY`           | Private key for the `prog-strength-backend-prod-keys` key pair. |
| `SEMANTIC_RELEASE_TOKEN`| Token the release workflow uses for the `@semantic-release/git` changelog push to `main`. `main` requires status checks the fresh release commit can't have; with `enforce_admins=false` an **admin** identity bypasses them. Use a fine-grained PAT (Repository → Contents: write) from an owner/admin account. The default `GITHUB_TOKEN` is not an admin and is rejected (GH006). |
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
| `AWS_ACCESS_KEY_ID`     | CI Terraform user.                                     |
| `AWS_SECRET_ACCESS_KEY` |                                                        |
| `EC2_HOST`              | Same EIP as the api repo (used by `deploy-caddy.yml`). |
| `EC2_SSH_KEY`           | Same key as the api repo.                              |

## Deployment flow

### On push to `prog-strength-api` `main`

`.github/workflows/release.yml`:

1. **release** job — semantic-release inspects commits since the last tag,
   bumps version, writes CHANGELOG, pushes the tag back to GitHub.
2. **deploy** job (runs only if a new release was published) — SSHes to
   the EC2 host as `ubuntu` and:
   - `cd /home/ubuntu/prog-strength-infra && git pull` — refreshes the
     Caddyfile mount target.
   - `cd /home/ubuntu/prog-strength-api && git fetch --tags && git checkout v<X.Y.Z>` — pins to the exact released commit.
   - Writes `.env` from the repository secrets (and `APP_VERSION=v<X.Y.Z>`,
     which the Dockerfile embeds into the binary via `-ldflags`).
   - `docker compose down && docker compose up --build -d`.

Commit type matters — `chore:` / `docs:` / `refactor:` won't cut a release
and so won't deploy. Use `feat:` for minor, `fix:` for patch.

### On push to `prog-strength-infra` `main` (Caddyfile changes only)

`.github/workflows/deploy-caddy.yml` triggers only when `caddy/**` changes:

1. SSHes to EC2, `git pull`s the infra repo so the bind-mount target is
   current.
2. `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`
   — in-place reload, preserves issued Let's Encrypt certs (in the
   `caddy_data` named volume) and live connections.

For any Terraform changes, the `apply.yml` workflow (in the infra repo)
handles them.

## Manual operations

### SSH

```sh
ssh -i prog-strength-backend-prod-keys.pem ubuntu@api.progstrength.fitness
```

If you get `REMOTE HOST IDENTIFICATION HAS CHANGED`, the instance was
rebuilt — clear the stale host key and reconnect:

```sh
ssh-keygen -R api.progstrength.fitness
ssh-keygen -R <elastic-ip>
```

Verify the new fingerprint against the AWS console output before
accepting:

```sh
aws ec2 get-console-output --instance-id <i-...> --region us-east-2 \
  --output text | grep -A1 "ECDSA\|ED25519"
```

### Manual deploy (skip GitHub Actions)

```sh
ssh -i prog-strength-backend-prod-keys.pem ubuntu@api.progstrength.fitness

cd /home/ubuntu/prog-strength-infra && git pull

cd /home/ubuntu/prog-strength-api
git fetch --tags --prune
git checkout v<X.Y.Z>     # or `main` for HEAD
docker compose down
docker compose up --build -d
docker compose logs --tail=50
```

`.env` is left in place by the last release deploy; if it's missing, copy
the contents from the repository secrets.

### Useful commands on EC2

```sh
cd /home/ubuntu/prog-strength-api

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

The SQLite DB lives at `/home/ubuntu/prog-strength-api/data/app.db` and is
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
   `bootstrap.sh` clones the repos and creates an empty `./data/`.
2. The first deploy SSHes in and runs `docker compose up -d`.
3. `restore` sees no local DB + a replica in S3 → downloads the latest
   snapshot into `/data/app.db`.
4. `api` starts against the restored DB.
5. `litestream` resumes replication; from this point on, S3 has a current
   replica again.

### Manual snapshot (still works)

```sh
# on EC2
cp data/app.db data/app.db.backup-$(date +%Y%m%d)

# or pull to your laptop
scp -i prog-strength-backend-prod-keys.pem \
  ubuntu@api.progstrength.fitness:~/prog-strength-api/data/app.db ./backup.db
```

### Required env vars

`LITESTREAM_REPLICA_BUCKET` and `LITESTREAM_REPLICA_REGION` must be present
in the host's `.env`. The release workflow writes `.env` from repository
secrets — add both there if they're missing.

## Troubleshooting

### Deploy fails with `permission denied (publickey)`

The `EC2_SSH_KEY` secret is wrong or missing in the repo secrets. Make
sure the entire private key (with header/footer) is pasted, and that
`EC2_HOST` is the Elastic IP, not a hostname that's drifted.

### Deploy succeeds but `api.progstrength.fitness` returns 502 / 503

Caddy is up but can't reach the api container. Check both containers:

```sh
docker compose ps             # api should be `Up (healthy)`
docker compose logs api       # look for migration / startup errors
```

If api is restarting in a loop, the most common cause is a missing or
malformed `.env` — rerun the deploy (or re-write `.env` manually from
secrets) and `docker compose up -d`.

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
