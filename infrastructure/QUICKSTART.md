# Quick Start: Deploy to Cloud Run with VPN

## Prerequisites
- Google Cloud CLI (`gcloud`) installed and authenticated
- Docker installed
- VPN credentials in `infrastructure/` directory:
  - `client.ovpn` ✓ (already present)
  - `auth.txt` ✓ (already present)

## Deployment Options

### Option A: Pulumi (Recommended - Infrastructure as Code)

### 1. Store VPN secrets
```bash
cd infrastructure
export GCP_PROJECT_ID="enterprise-automation-352103"
./setup-secrets.sh
```

### 2. Build and push image
```bash
./build-and-push.sh
```

### 3. Deploy with Pulumi
```bash
pulumi up
```

### Option B: Direct gcloud Deployment

## Deploy in 3 Steps

### 1. Set your GCP project
```bash
export GCP_PROJECT_ID="your-project-id"
export GCP_REGION="us-central1"  # optional, defaults to us-central1
```

### 2. Store VPN credentials securely
```bash
cd infrastructure
./setup-secrets.sh
```

### 3. Build and deploy
```bash
./cloudrun-deploy.sh
```

That's it! Your service will:
1. Connect to OpenVPN before starting
2. Route all traffic through the VPN tunnel
3. Start picoclaw-gateway gateway once VPN is established

## Verify Deployment

```bash
# Check service status
gcloud run services describe picoclaw-gateway --region=us-central1

# View logs to confirm VPN connection
gcloud run services logs read picoclaw-gateway --region=us-central1 --limit=30
```

Look for this in the logs:
```
Starting OpenVPN connection...
Waiting for VPN tunnel to establish...
VPN connected successfully! Tunnel IP: 10.8.0.X
VPN connection established. Starting application...
```

## What Gets Deployed

- **Image**: `gcr.io/${PROJECT_ID}/picoclaw-gateway:latest`
- **Resources**: 2 vCPU, 2GB RAM
- **Port**: 18790 (picoclaw-gateway gateway)
- **Auto-scaling**: 0-10 instances
- **VPN**: System-wide via tun0 interface

## Update VPN Credentials

To rotate or update VPN credentials:

```bash
# Update local files: infrastructure/client.ovpn or infrastructure/auth.txt
# Then re-run:
./setup-secrets.sh
./cloudrun-deploy.sh  # Redeploy to pick up new secrets
```

## Troubleshooting

**"VPN connection timeout"**
- Check VPN server is reachable from Cloud Run
- Verify credentials in Secret Manager
- Review logs: `gcloud run services logs read picoclaw-gateway`

**"Permission denied" for TUN device**
- Ensure using execution-environment=gen2 (set in cloudrun-deploy.sh)
- Cloud Run has kernel operation limitations

**Secrets not found**
- Run `./setup-secrets.sh` first
- Verify: `gcloud secrets list --project=${GCP_PROJECT_ID}`

## Next Steps

- Read [CLOUDRUN_VPN.md](CLOUDRUN_VPN.md) for detailed architecture
- Configure custom domain with `gcloud run domain-mappings`
- Set up Cloud Monitoring alerts
- Enable Cloud Armor for DDoS protection

## Cost Estimate

With default settings (auto-scale to zero):
- **Idle**: $0/month (scales to zero)
- **Active**: ~$0.10/hour when serving requests
- **Secrets**: $0.06/month per secret (2 secrets)

Total: Pay only when used, ~$0.12/month baseline for secrets.
