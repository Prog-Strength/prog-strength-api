# Deployment Guide - Prog Strength

This guide walks through deploying Prog Strength to AWS EC2 with automated GitHub Actions.

## Architecture

- **Single EC2 Instance** (t3.micro or t4g.nano recommended)
- **Docker Compose** running the API container
- **SQLite** database persisted to EBS volume
- **GitHub Actions** for automatic deployment on push to `main`

---

## One-Time EC2 Setup

### 1. Launch EC2 Instance

**AWS Console:**
1. Go to EC2 → Launch Instance
2. Choose **Ubuntu Server 24.04 LTS**
3. Instance type: **t3.micro** (Free tier) or **t4g.nano** (cheapest ARM)
4. Create or select a key pair (download the `.pem` file - you'll need it!)
5. Security Group - allow:
   - **SSH (22)** from your IP
   - **HTTP (8080)** from anywhere (0.0.0.0/0)
6. Storage: **8-10 GB** (default is fine)
7. Launch instance

**Note down:**
- Public IP address (e.g., `54.123.45.67`)
- Key pair name

### 2. SSH Into Your Instance

```bash
# Set key permissions
chmod 400 ~/Downloads/your-key.pem

# Connect
ssh -i ~/Downloads/your-key.pem ubuntu@54.123.45.67
```

### 3. Install Docker

Run these commands on your EC2 instance:

```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install Docker
sudo apt install -y docker.io docker-compose-v2

# Add ubuntu user to docker group (so you don't need sudo)
sudo usermod -aG docker ubuntu

# Log out and back in for group change to take effect
exit
```

SSH back in:
```bash
ssh -i ~/Downloads/your-key.pem ubuntu@54.123.45.67
```

Verify Docker works:
```bash
docker --version
docker compose version
```

### 4. Clone Your Repository

```bash
# Clone the repo
git clone https://github.com/jwallace145/prog-strength.git

# Navigate to project
cd prog-strength

# Create data directory for SQLite
mkdir -p data
```

### 5. Initial Deployment

```bash
# Build and start containers
docker compose up -d

# Check logs
docker compose logs -f

# Verify it's running
curl http://localhost:8080/health
```

You should see: `OK`

### 6. Test from Your Local Machine

```bash
# From your laptop (replace with your EC2 IP)
curl http://54.123.45.67:8080/health
```

If this works, your API is live! 🎉

---

## GitHub Actions Setup

### 1. Add SSH Key to GitHub Secrets

**Get your EC2 private key content:**
```bash
# On your laptop
cat ~/Downloads/your-key.pem
```

Copy the entire output (including `-----BEGIN RSA PRIVATE KEY-----` and `-----END RSA PRIVATE KEY-----`)

**Add to GitHub:**
1. Go to https://github.com/jwallace145/prog-strength/settings/secrets/actions
2. Click **New repository secret**
3. Name: `EC2_SSH_KEY`
4. Value: Paste the entire private key
5. Click **Add secret**

### 2. Add EC2 Host to GitHub Secrets

1. Click **New repository secret**
2. Name: `EC2_HOST`
3. Value: Your EC2 public IP (e.g., `54.123.45.67`)
4. Click **Add secret**

### 3. Enable GitHub Actions

The workflow file is already in `.github/workflows/deploy.yml`.

**Test the workflow:**
1. Make a small change (e.g., edit README.md)
2. Commit and push to `main`:
   ```bash
   git add .
   git commit -m "test: trigger deployment"
   git push origin main
   ```
3. Go to https://github.com/jwallace145/prog-strength/actions
4. Watch the deployment run!

---

## Deployment Process

Every time you push to `main`, GitHub Actions will:

1. SSH into your EC2 instance
2. Run `git pull origin main` to get latest code
3. Run `docker compose down` to stop old containers
4. Run `docker compose up --build -d` to rebuild and start
5. Show you the logs

**Deployment takes ~30-60 seconds** (build time).

---

## Manual Deployment

If you need to deploy manually (without pushing to GitHub):

```bash
# SSH to EC2
ssh -i ~/Downloads/your-key.pem ubuntu@54.123.45.67

# Navigate to project
cd prog-strength

# Pull latest changes
git pull origin main

# Restart services
docker compose down
docker compose up --build -d

# Check logs
docker compose logs -f
```

---

## Useful Commands on EC2

```bash
# Check if containers are running
docker compose ps

# View logs
docker compose logs -f

# Restart services
docker compose restart

# Stop services
docker compose down

# Rebuild and restart
docker compose up --build -d

# Check disk space (SQLite grows over time)
df -h

# Check database size
du -h data/app.db

# Access the container shell (for debugging)
docker compose exec api sh
```

---

## Database Backups

Your SQLite database lives at `/home/ubuntu/prog-strength/data/app.db` on EC2.

**Manual backup:**
```bash
# On EC2
cp data/app.db data/app.db.backup-$(date +%Y%m%d)

# Or download to your laptop
scp -i ~/Downloads/your-key.pem ubuntu@54.123.45.67:~/prog-strength/data/app.db ./backup.db
```

**Automated backups (future):**
- Add Litestream to `docker-compose.yml` to continuously backup to S3
- Or set up a cron job to copy `app.db` to S3 daily

---

## Troubleshooting

### Deployment fails with "Permission denied"

Your SSH key isn't set up correctly in GitHub Secrets. Make sure you copied the **entire** private key including header/footer.

### Can't access API from browser

Check EC2 Security Group allows port 8080 from anywhere (0.0.0.0/0).

### Database is empty after restart

Make sure the `data/` directory exists and `docker-compose.yml` has the volume mount:
```yaml
volumes:
  - ./data:/data
```

### Out of disk space

```bash
# Clean up old Docker images
docker system prune -a

# Check database size
du -h data/app.db
```

### Migrations not running

Check the logs:
```bash
docker compose logs api | grep migration
```

---

## Monitoring

**Check if API is healthy:**
```bash
curl http://54.123.45.67:8080/health
```

**Watch real-time logs:**
```bash
# SSH to EC2
cd prog-strength
docker compose logs -f
```

**See resource usage:**
```bash
docker stats
```

---

## Cost Estimate

**AWS Free Tier (first 12 months):**
- t3.micro: **Free** (750 hours/month)
- EBS: **Free** (30 GB)
- Data transfer: **Free** (15 GB out)

**After free tier:**
- t3.micro: ~$7/month
- t4g.nano (ARM): ~$3/month (cheaper!)
- EBS (10 GB): ~$1/month

**Total: ~$4-8/month**

---

## Next Steps

Once this is working:

1. **Get a domain name** (when you decide on branding)
2. **Add HTTPS with Caddy** (3 lines in docker-compose.yml)
3. **Set up Litestream** for automatic S3 backups
4. **Add monitoring** (uptime checks, error alerts)

For now, you have a production API running for $0-8/month! 🚀
