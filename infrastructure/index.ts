import * as pulumi from "@pulumi/pulumi";
import * as gcp from "@pulumi/gcp";

const config = new pulumi.Config();
const gcpConfig = new pulumi.Config("gcp");

const project = gcpConfig.require("project");
const region = config.get("region") ?? "asia-southeast1";
const imageRegion = config.get("imageRegion") ?? "europe-west4";
const imageTag = config.get("imageTag") ?? "latest";
const imageName = config.get("imageName") ?? "picoclaw";

// NOTE: Build image with OpenVPN support using: docker build -f docker/Dockerfile.cloudrun
const PICOCLAW_IMAGE = pulumi.interpolate`${imageRegion}-docker.pkg.dev/enterprise-automation-352103/container-repo/${imageName}:${imageTag}`;

// ─────────────────────────────────────────────
// Look up pre-existing secrets in Secret Manager
// ─────────────────────────────────────────────
const awsAccessKeySecret = gcp.secretmanager.Secret.get(
    "picoclaw-aws-access-key-id",
    `projects/${project}/secrets/PICOCLAW_AWS_ACCESS_KEY_ID`,
);

const awsSecretKeySecret = gcp.secretmanager.Secret.get(
    "picoclaw-aws-secret-access-key",
    `projects/${project}/secrets/PICOCLAW_AWS_SECRET_ACCESS_KEY`,
);

const awsRegionNameSecret = gcp.secretmanager.Secret.get(
    "picoclaw-aws-region-name",
    `projects/${project}/secrets/PICOCLAW_AWS_REGION_NAME`,
);

const launcherTokenSecret = gcp.secretmanager.Secret.get(
    "picoclaw-launcher-token",
    `projects/${project}/secrets/PICOCLAW_LAUNCHER_TOKEN`,
);

const vpnConfigSecret = gcp.secretmanager.Secret.get(
    "picoclaw-openvpn-config",
    `projects/${project}/secrets/PICOCLAW_OPENVPN_CONFIG`,
);

const vpnAuthSecret = gcp.secretmanager.Secret.get(
    "picoclaw-openvpn-auth",
    `projects/${project}/secrets/PICOCLAW_OPENVPN_AUTH`,
);

// ─────────────────────────────────────────────
// Dedicated service account for the Cloud Run service
// ─────────────────────────────────────────────
const gatewayServiceAccount = new gcp.serviceaccount.Account("picoclaw-gateway-sa", {
    project,
    accountId: "picoclaw-gateway",
    displayName: "PicoClaw Gateway Service Account",
});

// Persistent storage for /root/.picoclaw on Cloud Run.
const picoclawStateBucket = new gcp.storage.Bucket("picoclaw-volume", {
    project,
    labels: {
        "do-not-delete": "true",
    },
    location: region.toUpperCase(),
    uniformBucketLevelAccess: true,
    forceDestroy: false,
});

// Grant the service account secretAccessor at the project level so it can
// read all pre-existing secrets without needing setIamPolicy on each one.
const iamSecretAccessor = new gcp.projects.IAMMember("picoclaw-sa-secret-accessor", {
    project,
    role: "roles/secretmanager.secretAccessor",
    member: pulumi.interpolate`serviceAccount:${gatewayServiceAccount.email}`,
});

const stateBucketObjectAdmin = new gcp.storage.BucketIAMMember("picoclaw-sa-state-bucket-object-admin", {
    bucket: picoclawStateBucket.name,
    role: "roles/storage.objectAdmin",
    member: pulumi.interpolate`serviceAccount:${gatewayServiceAccount.email}`,
});

const artifactRegistryReader = new gcp.projects.IAMMember("picoclaw-sa-artifact-registry-reader", {
    project,
    role: "roles/artifactregistry.reader",
    member: pulumi.interpolate`serviceAccount:${gatewayServiceAccount.email}`,
});

// ─────────────────────────────────────────────
// Firewall rule to allow traffic to picoclaw gateway
// ─────────────────────────────────────────────
const gatewayFirewall = new gcp.compute.Firewall("picoclaw-gateway-firewall", {
    project,
    network: "default",
    allows: [
        {
            protocol: "tcp",
            ports: ["18800"],
        },
    ],
    sourceRanges: ["0.0.0.0/0"],
    targetTags: ["picoclaw-gateway"],
});

// ─────────────────────────────────────────────
// Compute Engine VM — picoclaw gateway
// ─────────────────────────────────────────────
const gatewayInstance = new gcp.compute.Instance("picoclaw-gateway", {
    project,
    zone: `${region}-a`,
    machineType: "n2-standard-2", // 2 vCPUs, 8GB RAM
    tags: ["picoclaw-gateway"],
    serviceAccount: {
        email: gatewayServiceAccount.email,
        scopes: ["cloud-platform"],
    },
    bootDisk: {
        initializeParams: {
            image: "cos-cloud/cos-stable", // Container-Optimized OS
            size: 30, // GB
        },
    },
    networkInterfaces: [
        {
            network: "default",
            accessConfigs: [{}], // Ephemeral external IP
        },
    ],
    metadata: {
        "gce-container-declaration": PICOCLAW_IMAGE.apply(image =>
            JSON.stringify({
                spec: {
                    containers: [
                        {
                            name: "picoclaw-gateway",
                            image,
                            ports: [
                                {
                                    containerPort: 18800,
                                    hostPort: 18800,
                                    protocol: "TCP",
                                },
                            ],
                            env: [
                                { name: "PICOCLAW_GATEWAY_HOST", value: "0.0.0.0" },
                                { name: "CHROME_FLAGS", value: "--disable-dev-shm-usage --no-sandbox --disable-setuid-sandbox --disable-gpu --disable-software-rasterizer --disable-extensions --disable-background-networking --disable-sync --disable-translate --disable-breakpad --disable-background-timer-throttling --disable-backgrounding-occluded-windows --disable-renderer-backgrounding --metrics-recording-only --mute-audio" },
                                { name: "PLAYWRIGHT_CHROMIUM_USE_HEADLESS_NEW", value: "1" },
                            ],
                            volumeMounts: [
                                {
                                    name: "picoclaw-home",
                                    mountPath: "/root/.picoclaw",
                                },
                                {
                                    name: "secrets",
                                    mountPath: "/run/secrets",
                                    readOnly: true,
                                },
                                {
                                    name: "openvpn-config",
                                    mountPath: "/etc/openvpn-config",
                                    readOnly: true,
                                },
                                {
                                    name: "openvpn-auth",
                                    mountPath: "/etc/openvpn-auth",
                                    readOnly: true,
                                },
                            ],
                            securityContext: {
                                privileged: true, // Required for OpenVPN
                            },
                        },
                    ],
                    volumes: [
                        {
                            name: "picoclaw-home",
                            hostPath: {
                                path: "/mnt/stateful_partition/picoclaw-home",
                            },
                        },
                        {
                            name: "secrets",
                            hostPath: {
                                path: "/mnt/stateful_partition/secrets",
                            },
                        },
                        {
                            name: "openvpn-config",
                            hostPath: {
                                path: "/mnt/stateful_partition/openvpn-config",
                            },
                        },
                        {
                            name: "openvpn-auth",
                            hostPath: {
                                path: "/mnt/stateful_partition/openvpn-auth",
                            },
                        },
                    ],
                    restartPolicy: "Always",
                },
            })
        ),
        "google-logging-enabled": "true",
        "startup-script": pulumi.all([
            project,
            picoclawStateBucket.name,
            awsAccessKeySecret.name,
            awsSecretKeySecret.name,
            awsRegionNameSecret.name,
            launcherTokenSecret.name,
            vpnConfigSecret.name,
            vpnAuthSecret.name,
        ]).apply(([proj, bucket, awsKeySecret, awsSecretSecret, awsRegionSecret, tokenSecret, vpnCfgSecret, vpnAuthSec]) => `#!/bin/bash
set -e

# Configure docker to authenticate with Artifact Registry using service account
docker-credential-gcr configure-docker --registries=europe-west4-docker.pkg.dev

# Container-Optimized OS already has gcsfuse installed
# Create mount point
mkdir -p /mnt/stateful_partition/picoclaw-home

# Mount GCS bucket
gcsfuse --implicit-dirs ${bucket} /mnt/stateful_partition/picoclaw-home

# Fetch secrets from Secret Manager and write to files for container
mkdir -p /mnt/stateful_partition/secrets
chmod 700 /mnt/stateful_partition/secrets

gcloud secrets versions access latest --secret=${awsKeySecret} --project=${proj} > /mnt/stateful_partition/secrets/aws_access_key_id
gcloud secrets versions access latest --secret=${awsSecretSecret} --project=${proj} > /mnt/stateful_partition/secrets/aws_secret_access_key
gcloud secrets versions access latest --secret=${awsRegionSecret} --project=${proj} > /mnt/stateful_partition/secrets/aws_region
gcloud secrets versions access latest --secret=${tokenSecret} --project=${proj} > /mnt/stateful_partition/secrets/launcher_token

# Create VPN config directory
mkdir -p /mnt/stateful_partition/openvpn-config /mnt/stateful_partition/openvpn-auth
chmod 700 /mnt/stateful_partition/openvpn-config /mnt/stateful_partition/openvpn-auth

# Fetch VPN secrets
gcloud secrets versions access latest --secret=${vpnCfgSecret} --project=${proj} > /mnt/stateful_partition/openvpn-config/client.ovpn
gcloud secrets versions access latest --secret=${vpnAuthSec} --project=${proj} > /mnt/stateful_partition/openvpn-auth/auth.txt
chmod 400 /mnt/stateful_partition/openvpn-config/client.ovpn /mnt/stateful_partition/openvpn-auth/auth.txt
`),
    },
}, {
    dependsOn: [iamSecretAccessor, stateBucketObjectAdmin, artifactRegistryReader, gatewayFirewall],
});

export const instanceName = gatewayInstance.name;
export const instanceIp = gatewayInstance.networkInterfaces.apply(
    interfaces => interfaces?.[0]?.accessConfigs?.[0]?.natIp ?? ""
);
export const serviceUrl = instanceIp.apply(ip => `http://${ip}:18800`);
export const serviceAccountEmail = gatewayServiceAccount.email;
