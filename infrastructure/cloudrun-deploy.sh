#!/bin/bash
set -e

# Cloud Run deployment script with OpenVPN support
# This deploys picoclaw to Cloud Run with system-wide VPN

PROJECT_ID="${GCP_PROJECT_ID:-your-project-id}"
REGION="${GCP_REGION:-us-central1}"
SERVICE_NAME="${SERVICE_NAME:-picoclaw}"
IMAGE_TAG="${IMAGE_TAG:-latest}"

echo "Building Docker image for Cloud Run with OpenVPN..."
docker build -f docker/Dockerfile.cloudrun -t gcr.io/${PROJECT_ID}/${SERVICE_NAME}:${IMAGE_TAG} .

echo "Pushing image to Google Container Registry..."
docker push gcr.io/${PROJECT_ID}/${SERVICE_NAME}:${IMAGE_TAG}

echo "Deploying to Cloud Run..."
gcloud run deploy ${SERVICE_NAME} \
  --image gcr.io/${PROJECT_ID}/${SERVICE_NAME}:${IMAGE_TAG} \
  --platform managed \
  --region ${REGION} \
  --project ${PROJECT_ID} \
  --allow-unauthenticated \
  --cpu 2 \
  --memory 2Gi \
  --timeout 3600 \
  --concurrency 80 \
  --min-instances 0 \
  --max-instances 10 \
  --port 18790 \
  --set-secrets="/etc/openvpn/client.ovpn=PICOCLAW_OPENVPN_CONFIG:latest,/etc/openvpn/auth.txt=PICOCLAW_OPENVPN_AUTH:latest"

echo ""
echo "Deployment complete!"
echo "Service URL: $(gcloud run services describe ${SERVICE_NAME} --region ${REGION} --project ${PROJECT_ID} --format='value(status.url)')"
