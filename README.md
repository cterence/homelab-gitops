# 🏠 homelab-gitops

<div style="display: flex; justify-content: left; flex-direction: row; align-items: center;">
<div><p>My Kubernetes cluster state. Managed by ArgoCD.</p><p>
<img alt="GitHub last commit" src="https://img.shields.io/github/last-commit/cterence/homelab">
<img alt="GitHub commit activity" src="https://img.shields.io/github/commit-activity/w/cterence/homelab">
<img alt="GitHub" src="https://img.shields.io/github/license/cterence/homelab">
</p></div>
</div>

## ⚙️ Hardware

| Device                    | Count | Specs                                          | Purpose         |
| ------------------------- | ----- | ---------------------------------------------- | --------------- |
| Lenovo ThinkCentre M75q-1 | 1     | Ryzen 5 Pro 3400GE + 16GB RAM + 512GB NVMe SSD | k8s master & worker node |

## k0s quick install

The install assumes that all external secrets are [already created in GitLab](https://external-secrets.io/latest/provider/gitlab-variables/).

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
