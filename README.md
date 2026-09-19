# 🏠 homelab-gitops

<div style="display: flex; justify-content: left; flex-direction: row; align-items: center;">
<div><p>My Kubernetes cluster managed with ArgoCD.</p><p>
<img alt="Health" src="https://status.terence.cloud/api/v1/endpoints/_homelab/health/badge.svg">
<img alt="Uptime" src="https://status.terence.cloud/api/v1/endpoints/_homelab/uptimes/7d/badge.svg">
</p></div>
</div>

## ⚙️ Hardware

| Device                    | Name     | Specs                                                                 | OS    | Role                       |
|---------------------------|----------|-----------------------------------------------------------------------|-------|----------------------------|
| Lenovo ThinkCentre M75q-2 | homelab2 | Ryzen 5 Pro 5650GE (6 core / 12 threads) / 24GB RAM / 256GB + 2TB SSD | NixOS | k8s controller+worker node |

## ✨ Features

- Kubernetes cluster deployed with [k0s](https://k0sproject.io/)
- GitOps deployment with [ArgoCD](https://argo-cd.readthedocs.io/en/stable/) and [Helm](https://helm.sh/)
- Simple flat directory structure: an [ApplicationSet](argocd-apps/applicationset.yaml) git generator deploys every umbrella Helm chart in [k8s-apps](/k8s-apps/) — an app is deployed by adding an (optionally empty) `appset.yaml` to its chart directory, undeployed by moving the directory to [k8s-apps/archive](/k8s-apps/archive/)
- Fully automated HTTPS exposition using [cert-manager](https://cert-manager.io/), [external-dns](https://kubernetes-sigs.github.io/external-dns) and [traefik](https://doc.traefik.io/traefik/)
- Authentication of sensitive apps with [PocketID](https://pocket-id.org/) as a passkey-only OIDC provider
- WAF using [ModSecurity plugin](https://plugins.traefik.io/plugins/644d9a72ebafd55c9c740848/mx-m-owasp-crs-modsecurity-plugin) and some [hacks](https://github.com/cterence/homelab-gitops/blob/a3fc90f9bab0287c901fd8f3cbab295a695b7658/k8s-apps/traefik/values.yaml#L78)
- Secrets management with [external-secrets](https://external-secrets.io/latest/), [OpenBao](https://openbao.org/) and [sops](https://github.com/getsops/sops) (check [terraform](/terraform/secrets/) for provisioning method)
- Offsite data backup using [Velero](https://velero.io/) and [Backblaze B2](https://www.backblaze.com/cloud-storage)
- Easy Backblaze-to-disk backup synchronization with [Kopia](https://kopia.io/)
- PostgreSQL database management with [CloudNativePG](https://cloudnative-pg.io/)
- Observability with [Prometheus](https://prometheus.io/), [Grafana](https://grafana.com/), [Loki](https://grafana.com/oss/loki/) and [Opentelemetry Collector](https://opentelemetry.io/docs/collector/)
- Alerting with [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) and a [Telegram Bot](https://prometheus.io/docs/alerting/latest/configuration/#telegram_config)
- Thorough HTTP / PostgreSQL status checks with [go-healthcheck](https://github.com/cterence/go-healthcheck) and [Gatus](https://gatus.io/)
- Automated updates with [Renovate](https://docs.renovatebot.com/) ([even linuxserver images!](/renovate.json5))
- Scale to zero using [Sablier](https://sablierapp.dev)
- Any app you'd want to host: [Nextcloud](https://nextcloud.com/fr/), [Immich](https://immich.app/), [Paperless-ngx](https://docs.paperless-ngx.com/) and more (see below)

## Workbench

[`workbench/`](workbench/) is where self-hosted programs are developed. Each
subdir holds source code, a `Dockerfile`, and a `build.yaml`. On commit, ArgoCD
renders a Kaniko Job that builds the image and pushes it to the in-cluster
registry — ready to deploy from [`k8s-apps/`](k8s-apps/). See
[`workbench/README.md`](workbench/README.md).

## 💻 What's currently deployed in my cluster ?

This is an [automatically updated](.github/workflows/update-deployed-apps.yaml) list of the apps deployed in my cluster. Click on an app to check its Helm configuration.

<!-- BEGIN deployed-apps -->
| App | Description |
| --- | --- |
| [anubis](./scripts/../k8s-apps/anubis) | Weighs the soul of incoming HTTP requests to stop AI crawlers |
| [argocd](./scripts/../k8s-apps/argocd) | Declarative, GitOps continuous delivery tool for Kubernetes |
| [arr-stack](./scripts/../k8s-apps/arr-stack) | Arr Stack |
| [audiobookshelf](./scripts/../k8s-apps/audiobookshelf) | Self-hosted audiobook and podcast server |
| [cert-manager](./scripts/../k8s-apps/cert-manager) | Automatically provision and manage TLS certificates in Kubernetes |
| [changedetection](./scripts/../k8s-apps/changedetection) | Website change detection, web page monitoring, and website change alerts |
| [cloudnative-pg](./scripts/../k8s-apps/cloudnative-pg) | CloudNativePG is a comprehensive platform designed to seamlessly manage PostgreSQL databases within Kubernetes environments, covering the entire operational lifecycle from initial deployment to ongoing maintenance |
| [cnpg-restore-test](./scripts/../k8s-apps/cnpg-restore-test) | Daily CNPG backup restore verification |
| [convertx](./scripts/../k8s-apps/convertx) | Self-hosted online file converter |
| [disk-usage-exporter](./scripts/../k8s-apps/disk-usage-exporter) | Per-directory disk usage Prometheus exporter for local-path volumes |
| [external-dns](./scripts/../k8s-apps/external-dns) | Configure external DNS servers (AWS Route53, Google CloudDNS and others) for Kubernetes Ingresses and Services |
| [external-secrets](./scripts/../k8s-apps/external-secrets) | External Secrets Operator reads information from a third-party service like AWS Secrets Manager and automatically injects the values as Kubernetes Secrets |
| [firefly](./scripts/../k8s-apps/firefly) | A free and open source personal finance manager |
| [gitea](./scripts/../k8s-apps/gitea) | Self-hosted Git service with a lightweight code hosting solution written in Go |
| [go-healthcheck](./scripts/../k8s-apps/go-healthcheck) | Simple HTTP healthchecks |
| [home-assistant](./scripts/../k8s-apps/home-assistant) | Open source home automation that puts local control and privacy first |
| [httpbin](./scripts/../k8s-apps/httpbin) | Echoes request data as JSON |
| [immich](./scripts/../k8s-apps/immich) | High performance self-hosted photo and video management solution |
| [it-tools](./scripts/../k8s-apps/it-tools) | Collection of handy online tools for developers |
| [kube-prometheus-stack](./scripts/../k8s-apps/kube-prometheus-stack) | kube-prometheus-stack collects Kubernetes manifests, Grafana dashboards, and Prometheus rules combined with documentation and scripts to provide easy to operate end-to-end Kubernetes cluster monitoring with Prometheus using the Prometheus Operator |
| [lastfm-scrobble-deduplicator](./scripts/../k8s-apps/lastfm-scrobble-deduplicator) | Periodically delete duplicate Last.fm scrobbles |
| [local-path-provisioner](./scripts/../k8s-apps/local-path-provisioner) | Utilize the local storage in each node |
| [loki](./scripts/../k8s-apps/loki) | Like Prometheus, but for logs |
| [mcp-telegram](./scripts/../k8s-apps/mcp-telegram) | Minimal Telegram notification MCP server |
| [metallb](./scripts/../k8s-apps/metallb) | A network load-balancer implementation for Kubernetes using standard routing protocols |
| [microbin](./scripts/../k8s-apps/microbin) | A secure, configurable file-sharing and URL shortening web app |
| [mosquitto](./scripts/../k8s-apps/mosquitto) | Open source MQTT broker |
| [nextcloud](./scripts/../k8s-apps/nextcloud) | A safe home for all your data |
| [niks3](./scripts/../k8s-apps/niks3) | S3-backed Nix binary cache with garbage collection |
| [openbao](./scripts/../k8s-apps/openbao) | Open source, community-driven fork of Vault managed by the Linux Foundation |
| [opentelemetry-collector](./scripts/../k8s-apps/opentelemetry-collector) | Vendor-agnostic implementation on how to receive, process and export telemetry data |
| [opentelemetry-operator](./scripts/../k8s-apps/opentelemetry-operator) | Kubernetes Operator for OpenTelemetry Collector |
| [paperless-ngx](./scripts/../k8s-apps/paperless-ngx) | Scan, index and archive all your physical documents |
| [pocket-id](./scripts/../k8s-apps/pocket-id) | Simple and easy-to-use OIDC provider that allows users to authenticate with their passkeys to your services |
| [registry](./scripts/../k8s-apps/registry) | In-cluster OCI image registry (CNCF distribution) |
| [reloader](./scripts/../k8s-apps/reloader) | A Kubernetes controller to watch changes in ConfigMap and Secrets and do rolling upgrades on Pods with their associated Deployment, StatefulSet, DaemonSet and DeploymentConfig |
| [sablier](./scripts/../k8s-apps/sablier) | A free and open-source software to start workloads on demand and stop them after a period of inactivity |
| [steam-headless](./scripts/../k8s-apps/steam-headless) | Headless Steam with Sunshine for Moonlight game streaming |
| [tailscale-operator](./scripts/../k8s-apps/tailscale-operator) | A Kubernetes Operator for Tailscale |
| [traefik](./scripts/../k8s-apps/traefik) | A Traefik based Kubernetes ingress controller |
| [tts9000](./scripts/../k8s-apps/tts9000) | Text-to-Speech service using Mistral's Voxtral TTS |
| [vaultwarden](./scripts/../k8s-apps/vaultwarden) | Unofficial Bitwarden compatible server written in Rust |
| [velero](./scripts/../k8s-apps/velero) | Backup and migrate Kubernetes applications and their persistent volumes |
| [versity-gw](./scripts/../k8s-apps/versity-gw) | High-performance S3 translation service |
| [workbench](./scripts/../k8s-apps/workbench) | ApplicationSet that builds images from workbench/* subdirs |
| [zigbee2mqtt](./scripts/../k8s-apps/zigbee2mqtt) | Zigbee to MQTT bridge |
<!-- END deployed-apps -->

<!-- e2e 4 -->
