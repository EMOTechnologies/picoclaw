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

// ─────────────────────────────────────────────
// VPC Network for routing traffic through VPN
// ─────────────────────────────────────────────
const vpcNetwork = new gcp.compute.Network("picoclaw-vpc", {
    project,
    name: "picoclaw-vpc",
    autoCreateSubnetworks: false,
    description: "VPC network for picoclaw with VPN gateway",
});

// Subnet in Singapore region for Cloud Run connector
const vpcSubnet = new gcp.compute.Subnetwork("picoclaw-subnet-singapore", {
    project,
    region: "asia-southeast1",
    name: "picoclaw-subnet-singapore",
    network: vpcNetwork.id,
    ipCidrRange: "10.8.0.0/28", // Small subnet, only 16 IPs
    privateIpGoogleAccess: true, // Enable Private Google Access for GCS, Secret Manager, etc.
    description: "Subnet for Serverless VPC Access connector in Singapore",
});

// Serverless VPC Access connector for Cloud Run
const vpcConnector = new gcp.vpcaccess.Connector("picoclaw-vpc-connector", {
    project,
    name: "picoclaw-connector",
    region: "asia-southeast1",
    network: vpcNetwork.name,
    ipCidrRange: "10.8.1.0/28", // Connector range (separate from subnet)
    minInstances: 2,
    maxInstances: 3,
    machineType: "e2-micro", // Cost-effective for low traffic
}, {
    dependsOn: [vpcNetwork, vpcSubnet],
});

// ─────────────────────────────────────────────
// Firewall rules for VPC
// ─────────────────────────────────────────────
const allowInternalFirewall = new gcp.compute.Firewall("allow-internal", {
    project,
    name: "picoclaw-allow-internal",
    network: vpcNetwork.id,
    description: "Allow internal traffic within VPC",
    allows: [
        {
            protocol: "tcp",
            ports: ["0-65535"],
        },
        {
            protocol: "udp",
            ports: ["0-65535"],
        },
        {
            protocol: "icmp",
        },
    ],
    sourceRanges: ["10.8.0.0/16"],
});

const allowOpenVPNFirewall = new gcp.compute.Firewall("allow-openvpn", {
    project,
    name: "picoclaw-allow-openvpn",
    network: vpcNetwork.id,
    description: "Allow outbound OpenVPN traffic to 195.133.132.5",
    direction: "EGRESS",
    allows: [
        {
            protocol: "udp",
            ports: ["1028"],
        },
    ],
    destinationRanges: ["195.133.132.5/32"],
});

// ─────────────────────────────────────────────
// Service account for VPN gateway VM
// ─────────────────────────────────────────────
const vpnGatewayServiceAccount = new gcp.serviceaccount.Account("vpn-gateway-sa", {
    project,
    accountId: "vpn-gateway",
    displayName: "VPN Gateway VM Service Account",
});

// Grant VM access to read VPN secrets
const vpnGatewaySecretAccessor = new gcp.projects.IAMMember("vpn-gateway-secret-accessor", {
    project,
    role: "roles/secretmanager.secretAccessor",
    member: pulumi.interpolate`serviceAccount:${vpnGatewayServiceAccount.email}`,
});

// ─────────────────────────────────────────────
// Routes needed BEFORE VM boots
// ─────────────────────────────────────────────
// Routes for package repositories (VM needs these to install OpenVPN)
const packageRepoRoutes = [
    { name: "route-fastly-cdn", range: "151.101.0.0/16" },        // Fastly CDN (Debian repos)
    { name: "route-google-pkgs", range: "64.233.160.0/19" },      // Google package repos
].map(r => new gcp.compute.Route(r.name, {
    project,
    name: r.name,
    network: vpcNetwork.id,
    destRange: r.range,
    priority: 100,
    nextHopGateway: "default-internet-gateway",
}, {
    dependsOn: [vpcNetwork],
}));

// Route for OpenVPN server itself (VM can't route to VPN through itself!)
const vpnServerRoute = new gcp.compute.Route("route-vpn-server", {
    project,
    name: "route-vpn-server",
    network: vpcNetwork.id,
    destRange: "195.133.132.5/32", // OpenVPN server
    priority: 100,
    nextHopGateway: "default-internet-gateway",
}, {
    dependsOn: [vpcNetwork],
});

// Additional Google API ranges for Secret Manager during VM boot
const googleApiBootRoutes = [
    { name: "route-google-apis-1", range: "142.250.0.0/15" },    // Google APIs
    { name: "route-google-apis-2", range: "172.217.0.0/16" },    // Google APIs
    { name: "route-google-apis-3", range: "216.239.32.0/19" },   // Google APIs
].map(r => new gcp.compute.Route(r.name, {
    project,
    name: r.name,
    network: vpcNetwork.id,
    destRange: r.range,
    priority: 100,
    nextHopGateway: "default-internet-gateway",
}, {
    dependsOn: [vpcNetwork],
}));

// Cloud Router for Cloud NAT (temporary bootstrap helper)
const cloudRouter = new gcp.compute.Router("picoclaw-router", {
    project,
    name: "picoclaw-router",
    region,
    network: vpcNetwork.id,
});

// Cloud NAT for VM bootstrap (allows VM to reach Secret Manager)
const cloudNat = new gcp.compute.RouterNat("picoclaw-nat", {
    project,
    name: "picoclaw-nat",
    region,
    router: cloudRouter.name,
    natIpAllocateOption: "AUTO_ONLY",
    sourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES",
});

// ─────────────────────────────────────────────
// VPN Gateway VM - Connects to OpenVPN server
// ─────────────────────────────────────────────
const vpnGatewayStartupScript = `#!/bin/bash
set -e

echo "Setting up OpenVPN client gateway..."

# Install OpenVPN client
apt-get update
apt-get install -y openvpn iptables iputils-ping

# Download VPN credentials from Secret Manager
echo "Downloading OpenVPN config from Secret Manager..."
gcloud secrets versions access latest --secret=PICOCLAW_OPENVPN_CONFIG > /etc/openvpn/client.ovpn
gcloud secrets versions access latest --secret=PICOCLAW_OPENVPN_AUTH > /etc/openvpn/auth.txt
chmod 600 /etc/openvpn/auth.txt

# Update config to use auth file
sed -i 's|auth-user-pass|auth-user-pass /etc/openvpn/auth.txt|g' /etc/openvpn/client.ovpn

# Remove Windows-only options
sed -i '/block-outside-dns/d' /etc/openvpn/client.ovpn

# Enable IP forwarding (required for NAT gateway)
echo "Enabling IP forwarding..."
echo "net.ipv4.ip_forward=1" >> /etc/sysctl.conf
sysctl -p

# Setup iptables NAT rules
# This forwards traffic from VPC (eth0) through VPN tunnel (tun0)
echo "Configuring iptables NAT rules..."
iptables -t nat -A POSTROUTING -o tun0 -j MASQUERADE
iptables -A FORWARD -i eth0 -o tun0 -j ACCEPT
iptables -A FORWARD -i tun0 -o eth0 -m state --state RELATED,ESTABLISHED -j ACCEPT

# Save iptables rules
DEBIAN_FRONTEND=noninteractive apt-get install -y iptables-persistent
netfilter-persistent save

# Start OpenVPN client (connects to 195.133.132.5:1028)
echo "Starting OpenVPN client connection to 195.133.132.5..."
systemctl enable openvpn@client
systemctl start openvpn@client

# Wait for tunnel
sleep 10

# Verify connection
if ip addr show tun0 &>/dev/null; then
    echo "✓ VPN tunnel established!"
    ip addr show tun0
else
    echo "✗ VPN tunnel failed to establish"
    journalctl -u openvpn@client -n 50
    exit 1
fi

echo "VPN gateway setup complete. Cloud Run traffic will route through this VM."
`;

const vpnGatewayInstance = new gcp.compute.Instance("vpn-gateway", {
    project,
    name: "vpn-gateway",
    zone: `${region}-a`,
    machineType: "e2-micro",
    canIpForward: true,
    tags: ["vpn-gateway"],
    bootDisk: {
        initializeParams: {
            image: "debian-cloud/debian-12",
        },
    },
    networkInterfaces: [
        {
            subnetwork: vpcSubnet.id,
            accessConfigs: [{}], // Ephemeral external IP for reaching OpenVPN server
        },
    ],
    serviceAccount: {
        email: vpnGatewayServiceAccount.email,
        scopes: ["https://www.googleapis.com/auth/cloud-platform"],
    },
    metadataStartupScript: vpnGatewayStartupScript,
    allowStoppingForUpdate: true,
}, {
    dependsOn: [vpcSubnet, vpnGatewaySecretAccessor, allowInternalFirewall, allowOpenVPNFirewall, vpnServerRoute, cloudNat, ...packageRepoRoutes, ...googleApiBootRoutes],
});

// ─────────────────────────────────────────────
// Routes for VPC traffic
// ─────────────────────────────────────────────
// Routes: VPN for internet, Private Google Access for GCS
// ─────────────────────────────────────────────

// Route for Private Google Access (keeps GCS/Secret Manager fast)
const googlePrivateAccessRoute = new gcp.compute.Route("route-google-apis", {
    project,
    name: "route-google-apis",
    network: vpcNetwork.id,
    destRange: "199.36.153.8/30", // private.googleapis.com range
    priority: 100,
    nextHopGateway: "default-internet-gateway",
}, {
    dependsOn: [vpcNetwork],
});

// Route for AWS Bedrock (prevents startup timeout)
const awsBedrockRoute = new gcp.compute.Route("route-aws-bedrock", {
    project,
    name: "route-aws-bedrock",
    network: vpcNetwork.id,
    destRange: "54.251.0.0/16", // AWS ap-southeast-1 (includes Bedrock)
    priority: 100,
    nextHopGateway: "default-internet-gateway",
}, {
    dependsOn: [vpcNetwork],
});

// Route ALL other internet traffic through VPN gateway
// TEMPORARILY DISABLED: VM can't bootstrap itself when all traffic routes through non-existent VPN
// const vpnGatewayDefaultRoute = new gcp.compute.Route("vpn-gateway-default", {
//     project,
//     name: "vpn-gateway-default",
//     network: vpcNetwork.id,
//     destRange: "0.0.0.0/0",
//     priority: 1000, // Lower priority than Google APIs route
//     nextHopInstance: vpnGatewayInstance.id,
// }, {
//     dependsOn: [vpnGatewayInstance],
// });

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
        executionEnvironment: "EXECUTION_ENVIRONMENT_GEN2",
        vpcAccess: {
            connector: vpcConnector.id,
            egress: "ALL_TRAFFIC", // Route ALL traffic through VPC
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
                ],
                resources: {
                    limits: {
                        cpu: "2",
                        memory: "2048Mi",  // Increased for Chromium browser automation
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
    dependsOn: [iamSecretAccessor, stateBucketObjectAdmin, vpcConnector, googlePrivateAccessRoute, awsBedrockRoute, ...packageRepoRoutes],
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

export const serviceUrl = gatewayService.uri;
export const serviceName = gatewayService.name;
export const serviceAccountEmail = gatewayServiceAccount.email;
export const vpcNetworkName = vpcNetwork.name;
export const vpcConnectorName = vpcConnector.name;
export const vpnGatewayInstanceName = vpnGatewayInstance.name;
export const vpnGatewayExternalIp = vpnGatewayInstance.networkInterfaces.apply(
    interfaces => interfaces?.[0]?.accessConfigs?.[0]?.natIp || "pending"
);
