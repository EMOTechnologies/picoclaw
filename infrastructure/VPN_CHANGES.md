# VPN Integration Changes Summary

## Files Modified

### 1. [index.ts](index.ts) - Pulumi Infrastructure
**Changes:**
- Added VPN secret references (`PICOCLAW_OPENVPN_CONFIG`, `PICOCLAW_OPENVPN_AUTH`)
- Set execution environment to GEN2 (required for OpenVPN)
- Added volume mounts for VPN config files to `/etc/openvpn/`
- Added comment to use `Dockerfile.cloudrun` for building

**Key additions:**
```typescript
// Secret references
const vpnConfigSecret = gcp.secretmanager.Secret.get(...)
const vpnAuthSecret = gcp.secretmanager.Secret.get(...)

// Gen2 execution environment
executionEnvironment: "EXECUTION_ENVIRONMENT_GEN2"

// Volume mounts for VPN files
volumeMounts: [
  { name: "vpn-config", mountPath: "/etc/openvpn/client.ovpn", subPath: "client.ovpn" },
  { name: "vpn-auth", mountPath: "/etc/openvpn/auth.txt", subPath: "auth.txt" }
]
```

## Files Created

### 2. [docker/Dockerfile.cloudrun](../docker/Dockerfile.cloudrun)
- Multi-stage build with OpenVPN installed
- Based on Alpine 3.23 with `openvpn`, `iptables`, `iproute2`
- Uses `vpn-entrypoint.sh` as main entrypoint

### 3. [docker/vpn-entrypoint.sh](../docker/vpn-entrypoint.sh)
- Validates VPN config and auth files exist
- Starts OpenVPN daemon
- Waits for `tun0` interface (30s timeout)
- Logs connection status
- Chains to original `entrypoint.sh` once VPN is up

### 4. [infrastructure/setup-secrets.sh](setup-secrets.sh)
- Creates/updates `PICOCLAW_OPENVPN_CONFIG` secret from `client.ovpn`
- Creates/updates `PICOCLAW_OPENVPN_AUTH` secret from `auth.txt`
- Grants service account access to secrets

### 5. [infrastructure/build-and-push.sh](build-and-push.sh)
- Builds image using `Dockerfile.cloudrun`
- Pushes to Artifact Registry
- Uses project defaults from Pulumi config

### 6. [infrastructure/cloudrun-deploy.sh](cloudrun-deploy.sh)
- Alternative gcloud-based deployment (bypasses Pulumi)
- Builds, pushes, and deploys in one command

### 7. Documentation
- [CLOUDRUN_VPN.md](CLOUDRUN_VPN.md) - Detailed architecture and troubleshooting
- [QUICKSTART.md](QUICKSTART.md) - Quick deployment guide with both methods
- [VPN_CHANGES.md](VPN_CHANGES.md) - This file

## Deployment Workflow

### Using Pulumi (Recommended):
```bash
cd infrastructure
./setup-secrets.sh          # Once: store VPN credentials
./build-and-push.sh         # Build & push VPN-enabled image
pulumi up                   # Deploy infrastructure
```

### Using gcloud directly:
```bash
cd infrastructure
./setup-secrets.sh
./cloudrun-deploy.sh
```

## How It Works

1. **Build**: `Dockerfile.cloudrun` creates image with OpenVPN
2. **Secrets**: VPN credentials stored in Secret Manager (not in image)
3. **Mount**: Pulumi mounts secrets as files at runtime
4. **Startup**: `vpn-entrypoint.sh` establishes VPN before app starts
5. **Traffic**: All container traffic routes through `tun0` tunnel

## Testing

Verify VPN connection in logs:
```bash
gcloud run services logs read picoclaw-gateway \
  --region=asia-southeast1 \
  --limit=50 | grep -i vpn
```

Expected output:
```
Starting OpenVPN connection...
Waiting for VPN tunnel to establish...
VPN connected successfully! Tunnel IP: 10.8.0.X
VPN connection established. Starting application...
```

## Rollback

To revert to non-VPN deployment:
1. Change `image:` in `index.ts` to use `docker/Dockerfile` (remove `.cloudrun`)
2. Remove VPN volume mounts and secrets
3. Run `pulumi up`

## Security Notes

- Secrets never in container images or logs
- VPN auth file has 0400 permissions
- Service account has minimal permissions
- Secrets stored in Secret Manager with automatic encryption
- Use secret rotation via `./setup-secrets.sh` (updates to new version)

## Next Steps

- [ ] Configure Pulumi stack with your project ID
- [ ] Run `setup-secrets.sh` to store VPN credentials
- [ ] Build image with `build-and-push.sh`
- [ ] Deploy with `pulumi up`
- [ ] Verify VPN connection in logs
- [ ] Test application connectivity through VPN
