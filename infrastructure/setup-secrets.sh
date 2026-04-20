#!/bin/bash
set -e

# Setup script to store OpenVPN credentials in Google Secret Manager
# This should be run once to configure secrets for Cloud Run

PROJECT_ID="${GCP_PROJECT_ID:-your-project-id}"

echo "Creating Secret Manager secrets for OpenVPN..."

# Create secret for OpenVPN config
echo "Creating secret: PICOCLAW_OPENVPN_CONFIG"
gcloud secrets create PICOCLAW_OPENVPN_CONFIG \
  --data-file=client.ovpn \
  --project=${PROJECT_ID} \
  --replication-policy="automatic" \
  || echo "Secret already exists, updating version..."

# Update if exists
gcloud secrets versions add PICOCLAW_OPENVPN_CONFIG \
  --data-file=client.ovpn \
  --project=${PROJECT_ID} \
  || true

# Create secret for OpenVPN auth
echo "Creating secret: PICOCLAW_OPENVPN_AUTH"
gcloud secrets create PICOCLAW_OPENVPN_AUTH \
  --data-file=auth.txt \
  --project=${PROJECT_ID} \
  --replication-policy="automatic" \
  || echo "Secret already exists, updating version..."

# Update if exists
gcloud secrets versions add PICOCLAW_OPENVPN_AUTH \
  --data-file=auth.txt \
  --project=${PROJECT_ID} \
  || true

# Grant Cloud Run service account access to secrets
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-$(gcloud iam service-accounts list --filter="email:*-compute@developer.gserviceaccount.com" --format="value(email)" --project=${PROJECT_ID})}"

echo "Granting access to service account: ${SERVICE_ACCOUNT}"

gcloud secrets add-iam-policy-binding PICOCLAW_OPENVPN_CONFIG \
  --member="serviceAccount:${SERVICE_ACCOUNT}" \
  --role="roles/secretmanager.secretAccessor" \
  --project=${PROJECT_ID}

gcloud secrets add-iam-policy-binding PICOCLAW_OPENVPN_AUTH \
  --member="serviceAccount:${SERVICE_ACCOUNT}" \
  --role="roles/secretmanager.secretAccessor" \
  --project=${PROJECT_ID}

echo ""
echo "Secrets configured successfully!"
echo "Run ./cloudrun-deploy.sh to deploy with VPN support"
