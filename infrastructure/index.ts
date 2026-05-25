import * as pulumi from "@pulumi/pulumi";
import * as gcp from "@pulumi/gcp";

const config = new pulumi.Config();
const gcpConfig = new pulumi.Config("gcp");

const project = gcpConfig.require("project");
const region = config.get("region") ?? "asia-southeast1";
const imageRegion = config.get("imageRegion") ?? "europe-west4";
const imageTag = config.get("imageTag") ?? "latest";
const imageName = config.get("imageName") ?? "picoclaw";

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

// ─────────────────────────────────────────────
// SSH Tunnel Proxy VM — routes traffic through office network
// Architecture: Cloud Run → GCP VM (e2-micro) → SSH tunnel → Office server
// Requires PICOCLAW_TUNNEL_SSH_PASSWORD secret in Secret Manager:
//   echo -n "<password>" | gcloud secrets create PICOCLAW_TUNNEL_SSH_PASSWORD --data-file=-
// ─────────────────────────────────────────────
const tunnelZone = config.get("tunnelZone") ?? `${region}-c`;
const tunnelSourceRanges = config.get("tunnelSourceRanges")?.split(",").map((s) => s.trim()).filter(Boolean) ?? ["0.0.0.0/0"];

const tunnelSshPasswordSecret = gcp.secretmanager.Secret.get(
    "picoclaw-tunnel-ssh-password",
    `projects/${project}/secrets/PICOCLAW_TUNNEL_SSH_PASSWORD`,
);

// Service account so the VM can read the SSH password from Secret Manager at boot
const tunnelServiceAccount = new gcp.serviceaccount.Account("ssh-tunnel-sa", {
    project,
    accountId: "ssh-tunnel-proxy",
    displayName: "SSH Tunnel Proxy Service Account",
});

const tunnelSaSecretAccessor = new gcp.projects.IAMMember("ssh-tunnel-sa-secret-accessor", {
    project,
    role: "roles/secretmanager.secretAccessor",
    member: pulumi.interpolate`serviceAccount:${tunnelServiceAccount.email}`,
});

// Static IP so the proxy URL stays stable across VM restarts
const tunnelStaticIp = new gcp.compute.Address("ssh-tunnel-proxy-ip", {
    project,
    region,
    name: "ssh-tunnel-proxy-ip",
});

// e2-micro VM — startup script fetches the SSH password from Secret Manager,
// writes it to /etc/ssh-tunnel.env, then starts the tunnel via sshpass + autossh.
const tunnelVm = new gcp.compute.Instance("ssh-tunnel-proxy", {
    project,
    zone: tunnelZone,
    name: "ssh-tunnel-proxy",
    machineType: "e2-micro",
    tags: ["proxy-server"],
    bootDisk: {
        initializeParams: {
            image: "debian-cloud/debian-12",
            size: 10,
        },
    },
    networkInterfaces: [
        {
            network: "default",
            accessConfigs: [
                {
                    natIp: tunnelStaticIp.address,
                },
            ],
        },
    ],
    serviceAccount: {
        email: tunnelServiceAccount.email,
        scopes: ["cloud-platform"],
    },
    metadata: {
        "startup-script": pulumi.interpolate`#!/bin/bash
export DEBIAN_FRONTEND=noninteractive
sudo -E dpkg --configure -a
sudo -E apt-get update -y
sudo -E apt-get install -y autossh openssh-client sshpass || exit 1

# Fetch password from Secret Manager — best-effort; IAM binding may not be ready on first boot.
# Re-run with: sudo google_metadata_script_runner startup
TOKEN=$(curl -sf -H "Metadata-Flavor: Google" \
  http://metadata.google.internal/computeMetadata/v1/instance/service-accounts/default/token \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])") || true

if [ -n "$TOKEN" ]; then
  TUNNEL_SSH_PASSWORD=$(curl -sf -H "Authorization: Bearer $TOKEN" \
    "https://secretmanager.googleapis.com/v1/projects/${project}/secrets/${tunnelSshPasswordSecret.secretId}/versions/latest:access" \
    | python3 -c "import sys,json,base64; print(base64.b64decode(json.load(sys.stdin)['payload']['data']).decode())") || true
  if [ -n "$TUNNEL_SSH_PASSWORD" ]; then
    echo "SSHPASS=$TUNNEL_SSH_PASSWORD" | sudo tee /etc/ssh-tunnel.env > /dev/null
    sudo chmod 600 /etc/ssh-tunnel.env
  fi
fi

sudo tee /etc/systemd/system/ssh-tunnel.service << UNIT
[Unit]
Description=SSH Tunnel to Office
After=network.target

[Service]
User=root
EnvironmentFile=/etc/ssh-tunnel.env
ExecStart=/usr/bin/sshpass -e autossh -M 0 -N -p 1036 -L 0.0.0.0:8080:localhost:8888 -o StrictHostKeyChecking=no -o PreferredAuthentications=password -o ServerAliveInterval=30 -o ServerAliveCountMax=3 -o ExitOnForwardFailure=yes picoclaw@195.133.132.5
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
UNIT

sudo systemctl daemon-reload
sudo systemctl enable ssh-tunnel

if [ -f /etc/ssh-tunnel.env ]; then
  sudo systemctl start ssh-tunnel
else
  echo "WARNING: /etc/ssh-tunnel.env missing — secret fetch failed. Run: sudo google_metadata_script_runner startup"
fi
`,
    },
}, {
    dependsOn: [tunnelSaSecretAccessor],
});

// Allow inbound TCP 8080 to VMs tagged proxy-server.
// Restrict tunnelSourceRanges via config in production (defaults to 0.0.0.0/0).
const tunnelFirewall = new gcp.compute.Firewall("allow-proxy-8080", {
    project,
    name: "allow-proxy-8080",
    network: "default",
    allows: [
        {
            protocol: "tcp",
            ports: ["8080"],
        },
    ],
    targetTags: ["proxy-server"],
    sourceRanges: tunnelSourceRanges,
});

// ─────────────────────────────────────────────
// Cloud Run v2 service — picoclaw gateway
// ─────────────────────────────────────────────
const gatewayService = new gcp.cloudrunv2.Service("picoclaw-gateway", {
    name: "picoclaw-gateway",
    location: region,
    project,
    ingress: "INGRESS_TRAFFIC_ALL",
    template: {
        serviceAccount: gatewayServiceAccount.email,
        scaling: {
            minInstanceCount: 0,
            maxInstanceCount: 3,
        },
        containers: [
            {
                image: PICOCLAW_IMAGE,
                ports: {
                    containerPort: 18800,
                },
                envs: [
                    { name: "PICOCLAW_GATEWAY_HOST", value: "0.0.0.0" },
                    // Chromium memory optimization flags
                    { name: "CHROME_FLAGS", value: "--disable-dev-shm-usage --no-sandbox --disable-setuid-sandbox --disable-gpu --disable-software-rasterizer --disable-extensions --disable-background-networking --disable-sync --disable-translate --disable-breakpad --disable-background-timer-throttling --disable-backgrounding-occluded-windows --disable-renderer-backgrounding --metrics-recording-only --mute-audio" },
                    { name: "PLAYWRIGHT_CHROMIUM_USE_HEADLESS_NEW", value: "1" },
                    {
                        name: "AWS_ACCESS_KEY_ID",
                        valueSource: {
                            secretKeyRef: {
                                secret: awsAccessKeySecret.secretId,
                                version: "latest",
                            },
                        },
                    },
                    {
                        name: "AWS_SECRET_ACCESS_KEY",
                        valueSource: {
                            secretKeyRef: {
                                secret: awsSecretKeySecret.secretId,
                                version: "latest",
                            },
                        },
                    },
                    {
                        name: "AWS_REGION",
                        valueSource: {
                            secretKeyRef: {
                                secret: awsRegionNameSecret.secretId,
                                version: "latest",
                            },
                        },
                    },
                    {
                        name: "AWS_DEFAULT_REGION",
                        valueSource: {
                            secretKeyRef: {
                                secret: awsRegionNameSecret.secretId,
                                version: "latest",
                            },
                        },
                    },
                    {
                        name: "PICOCLAW_LAUNCHER_TOKEN",
                        valueSource: {
                            secretKeyRef: {
                                secret: launcherTokenSecret.secretId,
                                version: "latest",
                            },
                        },
                    },
                    { name: "HTTP_PROXY", value: pulumi.interpolate`http://${tunnelStaticIp.address}:8080` },
                    { name: "HTTPS_PROXY", value: pulumi.interpolate`http://${tunnelStaticIp.address}:8080` },
                ],
                resources: {
                    limits: {
                        cpu: "1",
                        memory: "1536Mi",  // Increased for Chromium browser automation
                    },
                    cpuIdle: true,
                },
                volumeMounts: [
                    {
                        name: "picoclaw-home",
                        mountPath: "/root/.picoclaw",
                    },
                ],
                // startupProbe: {
                //     httpGet: {
                //         path: "/health",
                //         port: 18790,
                //     },
                //     initialDelaySeconds: 5,
                //     periodSeconds: 10,
                //     failureThreshold: 6,
                // },
                // livenessProbe: {
                //     httpGet: {
                //         path: "/health",
                //         port: 18790,
                //     },
                //     periodSeconds: 30,
                //     failureThreshold: 3,
                // },
            },
        ],
        volumes: [
            {
                name: "picoclaw-home",
                gcs: {
                    bucket: picoclawStateBucket.name,
                    readOnly: false,
                },
            },
        ],
    },
}, {
    dependsOn: [iamSecretAccessor, stateBucketObjectAdmin, tunnelVm],
});

// Temporarily disable access filtering and allow unauthenticated access.
// Previous filtered access logic:
// const runInvokerMembers =
//     config.get("runInvokerMembers")?.split(",").map((s) => s.trim()).filter(Boolean) ?? [];
// runInvokerMembers.forEach((member, i) => {
//     new gcp.cloudrunv2.ServiceIamMember(`picoclaw-gateway-invoker-${i}`, {
//         project,
//         location: region,
//         name: gatewayService.name,
//         role: "roles/run.invoker",
//         member,
//     });
// });
new gcp.cloudrunv2.ServiceIamMember("picoclaw-gateway-public-invoker", {
    project,
    location: region,
    name: gatewayService.name,
    role: "roles/run.invoker",
    member: "allUsers",
});

export const tunnelVmExternalIp = tunnelStaticIp.address;
export const tunnelVmZone = tunnelVm.zone;
export const tunnelFirewallId = tunnelFirewall.id;

export const serviceUrl = gatewayService.uri;
export const serviceName = gatewayService.name;
export const serviceAccountEmail = gatewayServiceAccount.email;
