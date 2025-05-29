# 🏠 homelab-gitops

<div style="display: flex; justify-content: left; flex-direction: row; align-items: center;">
<div><p>My Kubernetes cluster managed with ArgoCD.</p><p>
<img alt="Health" src="https://status.terence.cloud/api/v1/endpoints/_homelab/health/badge.svg">
<img alt="Uptime" src="https://status.terence.cloud/api/v1/endpoints/_homelab/uptimes/7d/badge.svg">
</p></div>
</div>

## ⚙️ Hardware (2 nodes)

| Device                    | Name     | Specs                                                                 | OS    | Role                       |
|---------------------------|----------|-----------------------------------------------------------------------|-------|----------------------------|
| Lenovo ThinkCentre M75q-2 | homelab2 | Ryzen 5 Pro 5650GE (6 core / 12 threads) / 24GB RAM / 256GB + 1TB SSD | NixOS | k8s controller+worker node |
| Lenovo ThinkCentre M75q-2 | homelab3 | Ryzen 5 Pro 5650GE (6 core / 12 threads) / 24GB RAM / 256GB + 1TB SSD | NixOS | k8s worker node            |

To access my apps, I expose them directly on the internet with port-forwarding on my router.

## ✨ Features

- Kubernetes cluster deployed with [k0s](https://k0sproject.io/)
- GitOps deployment with [ArgoCD](https://argo-cd.readthedocs.io/en/stable/) and [Helm](https://helm.sh/)
- Simple flat directory structure: [argocd-apps](/argocd-apps/) contains ArgoCD applications deploying umbrella Helm charts in [k8s-apps](/k8s-apps/)
- Fully automated HTTPS exposition using [cert-manager](https://cert-manager.io/), [external-dns](https://kubernetes-sigs.github.io/external-dns) and [ingress-nginx](https://kubernetes.github.io/ingress-nginx/)
- Authentication of sensitive apps with [oauth2-proxy](https://oauth2-proxy.github.io/oauth2-proxy/) with GitLab as an [OAuth2 provider](https://oauth2-proxy.github.io/oauth2-proxy/configuration/providers/gitlab/)
- Free endpoint security using [Crowdsec](https://www.crowdsec.net/)
- Secrets management with [external-secrets](https://external-secrets.io/latest/) and [GitLab CI/CD variables](https://external-secrets.io/latest/provider/gitlab-variables/)
- Dynamic volume provisioning and synchronous replication across nodes with [Longhorn](https://longhorn.io/)
- Offsite data backup using [Velero](https://velero.io/) and [Backblaze B2](https://www.backblaze.com/cloud-storage)
- Easy Backblaze-to-disk backup synchronization with [Kopia](https://kopia.io/) and a [custom script](https://github.com/cterence/nixos-config/blob/c95b2c0713a2472b11b2060ed28f6d4de75208f0/hosts/homelab2/home.nix#L120)
- PostgreSQL database management with [CloudNativePG](https://cloudnative-pg.io/)
- Observability with [Prometheus](https://prometheus.io/), [Grafana](https://grafana.com/), [Loki](https://grafana.com/oss/loki/) and [Opentelemetry Collector](https://opentelemetry.io/docs/collector/)
- Alerting with [Alertmanager](https://prometheus.io/docs/alerting/latest/alertmanager/) and a [Telegram Bot](https://prometheus.io/docs/alerting/latest/configuration/#telegram_config)
- Thorough HTTP / PostgreSQL status checks with [go-healthcheck](https://github.com/cterence/go-healthcheck) and [Gatus](https://gatus.io/)
- Automated updates with [Renovate](https://docs.renovatebot.com/) ([even linuxserver images!](/renovate.json5))
- Any app you'd want to host! Currently, [Nextcloud](https://nextcloud.com/fr/), [Immich](https://immich.app/), [Paperless-ngx](https://docs.paperless-ngx.com/) and more (see below)

## 💻 What's currently deployed in my cluster ?

This is an [automatically updated](.github/workflows/update-deployed-apps.yaml) list of the apps I have configured and/or deployed. Click on an app to check its Helm configuration.

<!-- BEGIN deployed-apps -->
| App | Description | Is deployed |
| --- | --- | --- |
| [argocd](./scripts/../k8s-apps/argocd) | Declarative, GitOps continuous delivery tool for Kubernetes | ✅ |
| [arr-stack](./scripts/../k8s-apps/arr-stack) | Arr Stack | ✅ |
| [blackbox-exporter](./scripts/../k8s-apps/blackbox-exporter) | Allows blackbox probing of endpoints over HTTP, HTTPS, DNS, TCP, ICMP and gRPC | ✅ |
| [calibre-web](./scripts/../k8s-apps/calibre-web) | Web app for browsing, reading and downloading eBooks stored in a Calibre database | ✅ |
| [cert-manager](./scripts/../k8s-apps/cert-manager) | Automatically provision and manage TLS certificates in Kubernetes | ✅ |
| [cloudnative-pg](./scripts/../k8s-apps/cloudnative-pg) | CloudNativePG is a comprehensive platform designed to seamlessly manage PostgreSQL databases within Kubernetes environments, covering the entire operational lifecycle from initial deployment to ongoing maintenance | ✅ |
| [convertx](./scripts/../k8s-apps/convertx) | Self-hosted online file converter | ✅ |
| [crowdsec](./scripts/../k8s-apps/crowdsec) | Open-source and participative security solution offering crowdsourced protection against malicious IPs and access to the most advanced real-world CTI | ✅ |
| [external-dns](./scripts/../k8s-apps/external-dns) | Configure external DNS servers (AWS Route53, Google CloudDNS and others) for Kubernetes Ingresses and Services | ✅ |
| [external-secrets](./scripts/../k8s-apps/external-secrets) | External Secrets Operator reads information from a third-party service like AWS Secrets Manager and automatically injects the values as Kubernetes Secrets | ✅ |
| [garage](./scripts/../k8s-apps/garage) | S3-compatible object store for small self-hosted geo-distributed deployments | ✅ |
| [go-healthcheck](./scripts/../k8s-apps/go-healthcheck) | Simple HTTP healthchecks | ✅ |
| [headscale](./scripts/../k8s-apps/headscale) | An open source, self-hosted implementation of the Tailscale control server | ❌ |
| [home-assistant](./scripts/../k8s-apps/home-assistant) | Open source home automation that puts local control and privacy first | ✅ |
| [homepage](./scripts/../k8s-apps/homepage) | A highly customizable homepage (or startpage / application dashboard) with Docker and service API integrations | ✅ |
| [httpbin](./scripts/../k8s-apps/httpbin) | Echoes request data as JSON | ✅ |
| [immich](./scripts/../k8s-apps/immich) | High performance self-hosted photo and video management solution | ✅ |
| [ingress-nginx](./scripts/../k8s-apps/ingress-nginx) | Ingress-NGINX Controller for Kubernetes | ✅ |
| [it-tools](./scripts/../k8s-apps/it-tools) | Collection of handy online tools for developers | ✅ |
| [kube-prometheus-stack](./scripts/../k8s-apps/kube-prometheus-stack) | kube-prometheus-stack collects Kubernetes manifests, Grafana dashboards, and Prometheus rules combined with documentation and scripts to provide easy to operate end-to-end Kubernetes cluster monitoring with Prometheus using the Prometheus Operator | ✅ |
| [loki](./scripts/../k8s-apps/loki) | Like Prometheus, but for logs | ✅ |
| [longhorn](./scripts/../k8s-apps/longhorn) | Cloud-Native distributed storage built on and for Kubernetes | ✅ |
| [mealie](./scripts/../k8s-apps/mealie) | Recipe manager and meal planner | ✅ |
| [metallb](./scripts/../k8s-apps/metallb) | A network load-balancer implementation for Kubernetes using standard routing protocols | ✅ |
| [microbin](./scripts/../k8s-apps/microbin) | A secure, configurable file-sharing and URL shortening web app | ✅ |
| [mosquitto](./scripts/../k8s-apps/mosquitto) | Open source MQTT broker | ✅ |
| [nextcloud](./scripts/../k8s-apps/nextcloud) | A safe home for all your data | ✅ |
| [oauth2-proxy](./scripts/../k8s-apps/oauth2-proxy) | A reverse proxy that provides authentication with Google, Azure, OpenID Connect and many more identity providers | ✅ |
| [opencloud](./scripts/../k8s-apps/opencloud) | Excellent file sharing | ✅ |
| [opentelemetry-collector](./scripts/../k8s-apps/opentelemetry-collector) | Vendor-agnostic implementation on how to receive, process and export telemetry data | ✅ |
| [opentelemetry-operator](./scripts/../k8s-apps/opentelemetry-operator) | Kubernetes Operator for OpenTelemetry Collector | ✅ |
| [paperless-ngx](./scripts/../k8s-apps/paperless-ngx) | Scan, index and archive all your physical documents | ✅ |
| [radicale](./scripts/../k8s-apps/radicale) | Free and Open-Source CalDAV and CardDAV Server | ✅ |
| [reloader](./scripts/../k8s-apps/reloader) | A Kubernetes controller to watch changes in ConfigMap and Secrets and do rolling upgrades on Pods with their associated Deployment, StatefulSet, DaemonSet and DeploymentConfig | ✅ |
| [satisfactory-server](./scripts/../k8s-apps/satisfactory-server) | Satisfactory server | ✅ |
| [snapshot-controller](./scripts/../k8s-apps/snapshot-controller) | Implements the control loop for CSI snapshot functionality | ✅ |
| [tailscale-operator](./scripts/../k8s-apps/tailscale-operator) | A Kubernetes Operator for Tailscale | ✅ |
| [vaultwarden](./scripts/../k8s-apps/vaultwarden) | Unofficial Bitwarden compatible server written in Rust | ✅ |
| [velero](./scripts/../k8s-apps/velero) | Backup and migrate Kubernetes applications and their persistent volumes | ✅ |
| [zigbee2mqtt](./scripts/../k8s-apps/zigbee2mqtt) | Zigbee to MQTT bridge | ✅ |
<!-- END deployed-apps -->

## 🏗️ k0s quick install

The install assumes that all external secrets are [already created in a GitLab project as CI/CD variables](https://external-secrets.io/latest/provider/gitlab-variables/).

Start the k0s cluster:

```bash
cd ~/homelab-gitops
sudo k0s install controller --enable-worker -c ./k0s.yaml
sudo k0s start
sleep 5
sudo k0s status
sudo k0s kubeconfig admin > ~/.kube/config
kubectl taint nodes --all node-role.kubernetes.io/master-
```

Create the GitLab token secret used by external-secrets:

```bash
kubectl create ns external-secrets
kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: gitlab-secret
  namespace: external-secrets
type: Opaque
stringData:
  token: xxx
```

Change the token value and type `<Ctrl+D>` `<Enter>` to create the secret.

Deploy external-secrets and ArgoCD apps:

```bash
cd ../../k8s-apps/external-secrets && helm dependency update && helm template external-secrets -n external-secrets . | kubectl apply -n external-secrets -f -
kubectl create ns argocd
cd ../../k8s-apps/argocd && helm dependency update && helm template argocd . -n argocd | kubectl apply -n argocd -f -
kubectl apply -f ../../argocd-apps/app-of-apps.yaml -n argocd
```

Cluster should be ready!

## 💣 Teardown

Save the GitLab token secret

  ```bash
  kubectl get secret -n external-secrets gitlab-secret -o yaml > gitlab-secret.yaml
  ```

Teardown the cluster

  ```bash
  sudo k0s stop
  sudo k0s reset -v -d
  ```
