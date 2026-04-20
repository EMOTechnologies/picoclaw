#!/bin/bash
set -e

# Build and push picoclaw image with OpenVPN support to Artifact Registry
# This script should be run from the project root

PROJECT_ID="${GCP_PROJECT_ID:-enterprise-automation-352103}"
IMAGE_REGION="${IMAGE_REGION:-europe-west4}"
IMAGE_NAME="${IMAGE_NAME:-picoclaw}"
IMAGE_TAG="${IMAGE_TAG:-latest}"

FULL_IMAGE="${IMAGE_REGION}-docker.pkg.dev/${PROJECT_ID}/container-repo/${IMAGE_NAME}:${IMAGE_TAG}"

echo "Building picoclaw with OpenVPN support..."
docker build -f docker/Dockerfile.cloudrun -t "${FULL_IMAGE}" .

echo "Pushing to Artifact Registry..."
docker push "${FULL_IMAGE}"

echo ""
echo "Image pushed: ${FULL_IMAGE}"
echo ""
echo "To deploy with Pulumi:"
echo "  cd infrastructure"
echo "  pulumi up"
