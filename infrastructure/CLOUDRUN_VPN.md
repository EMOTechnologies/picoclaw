# Cloud Run with System-Wide OpenVPN

This setup enables picoclaw to run on Google Cloud Run with a system-wide OpenVPN connection established before the application starts.

## Architecture

1. **VPN-Aware Dockerfile** ([Dockerfile.cloudrun](../docker/Dockerfile.cloudrun))
   - Based on Alpine Linux with OpenVPN installed
   - Includes networking tools (iptables, iproute2)
   - Multi-stage build for minimal image size

2. **VPN Entrypoint** ([vpn-entrypoint.sh](../docker/vpn-entrypoint.sh))
   - Starts OpenVPN daemon before application
   - Waits for tunnel (tun0) to establish
   - Verifies connectivity before proceeding
   - Logs VPN status to /var/log/openvpn.log

3. **Secret Management**
   - OpenVPN config and credentials stored in Google Secret Manager
   - Mounted securely at runtime via Cloud Run secrets
   - No credentials in container image

## Setup Instructions

### 1. Store VPN Credentials in Secret Manager

```bash
# Set your GCP project
export GCP_PROJECT_ID="your-project-id"

# Upload secrets (run once)
cd infrastructure
chmod +x setup-secrets.sh
./setup-secrets.sh
```

This creates two secrets:
- `PICOCLAW_OPENVPN_CONFIG` → client.ovpn file
- `PICOCLAW_OPENVPN_AUTH` → auth.txt (username/password)

### 2. Deploy to Cloud Run

```bash
# Set deployment variables
export GCP_PROJECT_ID="your-project-id"
export GCP_REGION="us-central1"
export SERVICE_NAME="picoclaw"

# Deploy
chmod +x cloudrun-deploy.sh
./cloudrun-deploy.sh
```

### 3. Verify VPN Connection

```bash
# Check logs
gcloud run services logs read picoclaw --region=us-central1 --limit=50

# Look for:
# - "Starting OpenVPN connection..."
# - "VPN connected successfully! Tunnel IP: X.X.X.X"
# - "VPN connection established. Starting application..."
```

## Cloud Run Requirements

### Privilege Mode
Cloud Run **Second Generation** execution environment is required for:
- TUN/TAP device creation
- Network namespace manipulation
- iptables rules

Deploy with:
```bash
gcloud run deploy picoclaw \
  --execution-environment=gen2 \
  --vpc-egress=all-traffic \
  ...
```

### Service Account Permissions
The Cloud Run service account needs:
- `roles/secretmanager.secretAccessor` - Read VPN secrets
- Standard Cloud Run permissions

### Network Configuration
- **VPC Connector**: Optional, but recommended for hybrid connectivity
- **Egress**: Set to `all-traffic` to route through VPN
- **Ingress**: Configure based on your access requirements

## Troubleshooting

### VPN Connection Timeout
If deployment fails with "VPN connection timeout":

1. Check OpenVPN logs:
```bash
gcloud run services logs read picoclaw --region=us-central1 | grep openvpn
```

2. Verify secrets are mounted:
```bash
# Add to vpn-entrypoint.sh for debugging:
ls -la /etc/openvpn/
```

3. Check VPN server accessibility from Cloud Run:
   - Ensure VPN server allows UDP 1028
   - Check Cloud Run egress routing

### Permission Denied Errors
If you see "Operation not permitted" for network operations:

1. Ensure using `--execution-environment=gen2`
2. Cloud Run has limitations on some kernel operations
3. Consider Cloud Run Jobs for more privileged operations

### Secrets Not Found
If secrets aren't accessible:

1. Verify secrets exist:
```bash
gcloud secrets list --project=${GCP_PROJECT_ID}
```

2. Check IAM permissions:
```bash
gcloud secrets get-iam-policy PICOCLAW_OPENVPN_CONFIG
```

## Local Testing

Test the Docker image locally:

```bash
# Build image
docker build -f docker/Dockerfile.cloudrun -t picoclaw-vpn:test .

# Run with VPN credentials
docker run -it --rm \
  --cap-add=NET_ADMIN \
  --device=/dev/net/tun \
  -v $(pwd)/infrastructure/client.ovpn:/etc/openvpn/client.ovpn:ro \
  -v $(pwd)/infrastructure/auth.txt:/etc/openvpn/auth.txt:ro \
  -p 18790:18790 \
  picoclaw-vpn:test
```

Note: `--cap-add=NET_ADMIN` and `--device=/dev/net/tun` are required for VPN locally.

## Security Considerations

1. **Secret Rotation**: Regularly rotate VPN credentials in Secret Manager
2. **Access Control**: Limit which services can access VPN secrets
3. **Network Isolation**: Use VPC Service Controls if handling sensitive data
4. **Audit Logging**: Enable Cloud Audit Logs for secret access
5. **Image Scanning**: Enable Container Analysis for vulnerability scanning

## Cost Optimization

- Use `--min-instances=0` for auto-scaling to zero
- VPN connection adds ~5-10s to cold start time
- Consider `--min-instances=1` if cold starts are problematic
- Monitor CPU/memory usage and adjust allocations

## Alternative Approaches

If Cloud Run limitations are too restrictive:

1. **Cloud Run Jobs**: More privileged execution for batch workloads
2. **GKE Autopilot**: Full Kubernetes with VPN sidecar containers
3. **Compute Engine**: VMs with full network control
4. **Cloud VPN/Interconnect**: Native GCP VPN solutions for hybrid connectivity
