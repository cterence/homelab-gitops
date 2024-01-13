# 🏠 homelab

<div style="display: flex; justify-content: left; flex-direction: row; align-items: center;">
<img width="144px" height="144px" style="margin-right: 10px;" src="https://camo.githubusercontent.com/fd23263fa81136afc1918aaee7bd61b0178989edb8c999e5dd6fd8bc7417932d/68747470733a2f2f692e696d6775722e636f6d2f45584e544a6e412e706e67"></img>
<div><p>My Kubernetes cluster state. Managed by ArgoCD.</p><p>
<img alt="GitHub last commit" src="https://img.shields.io/github/last-commit/cterence/homelab">
<img alt="GitHub commit activity" src="https://img.shields.io/github/commit-activity/w/cterence/homelab">
<img alt="GitHub" src="https://img.shields.io/github/license/cterence/homelab">
</p></div>
</div>

## ⚙️ Hardware

| Device                    | Count | Specs                                          | Purpose         |
| ------------------------- | ----- | ---------------------------------------------- | --------------- |
| Lenovo ThinkCentre M75q-1 | 1     | Ryzen 5 Pro 3400GE + 16GB RAM + 512GB NVMe SSD | All-in-one node |

I also use this machine to host a Nextcloud instance for my files.

## 🌐 Network topology

Here's a macroscopic overview of the state of my network, connecting all my devices together, including this lab.

![network](./assets/topology.excalidraw.png)

## k0s quick install

I use k0s for installation now, it's much easier to setup and maintain.

The install assumes that all external secrets are already created in GitLab.

```bash
sudo k0s install controller --enable-worker -c ./k0s.yaml
sudo k0s start
sudo k0s status
sudo k0s kubeconfig admin > ~/.kube/config
```

Create cilium

```bash
cd k8s-apps/cilium && helm dependency update && helm template cilium . | kubectl apply -n kube-system -f -
```

Create the GitLab token secret used by external-secrets

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
# Enter + CTRL+D
```

Create external-secrets

```bash
cd ../../k8s-apps/external-secrets && helm dependency update && helm template external-secrets . | kubectl apply -n external-secrets -f -
```

Create argocd and app of apps

```bash
cd ../../k8s-apps/argocd && helm dependency update && helm template argocd . | kubectl apply -n argocd -f -
kubectl apply -f ../../argocd-apps/app-of-apps.yaml -n argocd
```

Cluster should be ready !

## 💣 Teardown

- Save the GitLab token secret

  ```bash
  kubectl get secret -n external-secrets gitlab-secret -o yaml > gitlab-secret.yaml
  ```

- Teardown the cluster

  ```bash
  sudo k0s stop
  sudo k0s reset -v -d
  ```
