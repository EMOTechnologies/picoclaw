import * as pulumi from "@pulumi/pulumi";
import * as gcp from "@pulumi/gcp";

const config = new pulumi.Config();
const gcpConfig = new pulumi.Config("gcp");

const project = gcpConfig.require("project");
const region = config.get("region") ?? "asia-southeast1";
const imageTag = config.get("imageTag") ?? "latest";
const imageName = config.get("imageName") ?? "picoclaw";

const PICOCLAW_IMAGE = pulumi.interpolate`${region}-docker.pkg.dev/enterprise-automation-352103/container-repo/${imageName}:${imageTag}`;

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

// Grant the service account secretAccessor at the project level so it can
// read all pre-existing secrets without needing setIamPolicy on each one.
const iamSecretAccessor = new gcp.projects.IAMMember("picoclaw-sa-secret-accessor", {
    project,
    role: "roles/secretmanager.secretAccessor",
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
            minInstanceCount: 1,
            maxInstanceCount: 3,
        },
        containers: [
            {
                image: PICOCLAW_IMAGE,
                ports: {
                    containerPort: 18790,
                },
                envs: [
                    { name: "PICOCLAW_GATEWAY_HOST", value: "0.0.0.0" },
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
                        cpu: "1",
                        memory: "512Mi",
                    },
                    cpuIdle: false,
                },
                startupProbe: {
                    httpGet: {
                        path: "/health",
                        port: 18790,
                    },
                    initialDelaySeconds: 5,
                    periodSeconds: 10,
                    failureThreshold: 6,
                },
                livenessProbe: {
                    httpGet: {
                        path: "/health",
                        port: 18790,
                    },
                    periodSeconds: 30,
                    failureThreshold: 3,
                },
            },
        ],
    },
}, {
    dependsOn: [iamSecretAccessor],
});

// Grant invoker access only to authenticated members of the current project
new gcp.cloudrunv2.ServiceIamBinding("picoclaw-gateway-invoker", {
    project,
    location: region,
    name: gatewayService.name,
    role: "roles/run.invoker",
    members: [
        `projectOwner:${project}`,
        `projectEditor:${project}`,
        `projectViewer:${project}`,
    ],
});

export const serviceUrl = gatewayService.uri;
export const serviceName = gatewayService.name;
export const serviceAccountEmail = gatewayServiceAccount.email;
