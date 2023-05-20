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

## ⏫ Bootstrap

How to setup a cluster just like mine with a Linux node and `kubeadm`.

### Cluster initialization

On your Kubernetes node :

- Create the cluster using `kubeadm`

  ```bash
  sudo kubeadm init \
    --pod-network-cidr "10.244.0.0/16" \
    --control-plane-endpoint "_external_ip_:6443" \
    --apiserver-cert-extra-sans "_external_domain_name_"
  ```

- Copy the admin kubeconfig to your home directory

  ```bash
  mkdir -p $HOME/.kube
  sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config
  sudo chown $(id -u):$(id -g) $HOME/.kube/config
  ```

- Install the flannel CNI

  ```bash
  kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml
  ```

- Remove the master node taint (if you are running a single node cluster)

  ```bash
  kubectl taint nodes _node_name_ node-role.kubernetes.io/master:NoSchedule-
  ```

### Important secrets creation

- Install sealed-secrets in your cluster

  ```bash
  helm repo add sealed-secrets https://bitnami-labs.github.io/sealed-secrets
  helm repo update
  helm install --wait sealed-secrets sealed-secrets/sealed-secrets -n sealed-secrets --create-namespace=true
  ```

- Install the `kubeseal` CLI

  - Arch

    ```bash
    yay -S kubeseal
    ```

  - Other OSes : see https://github.com/bitnami-labs/sealed-secrets#installation

- OPTIONAL: declare a shell function to seal a secret

  ```bash
  seal () {
    kubeseal \
      --controller-name=sealed-secrets \
      --controller-namespace sealed-secrets \
      --format yaml \
      < applications/"$1"/"$2"-cleartext.yaml \
      > applications/"$1"/"$2".yaml \
      && rm applications/"$1"/"$2"-cleartext.yaml
  }
  ```

- **Repository credentials**

  - Generate an ssh key

    ```bash
    ssh-keygen -t ed25519 -C "ArgoCD" -f argocd -N ""
    ```

  - Add the public ssh key to your git remote repository (for Github, add a [deploy key](https://docs.github.com/en/developers/overview/managing-deploy-keys#deploy-keys))

  - Create a temporary secret manifest named `applications/argocd/argo-github-repository-credentials-cleartext.yaml` from the ssh private key

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      name: argo-github-repository-credentials
      namespace: argocd
      labels:
        argocd.argoproj.io/secret-type: repository
    stringData:
      sshPrivateKey: |
        -----BEGIN OPENSSH PRIVATE KEY-----
        _key_material_
        -----END OPENSSH PRIVATE KEY-----
      url: _your_repo_ssh_uri_
    ```

  - Delete the ssh keys

    ```bash
    rm argocd argocd.pub
    ```

  - Seal the secret

    ```bash
    seal argocd argo-github-repository-credentials
    # OR
    kubeseal \
      --controller-name=sealed-secrets \
      --controller-namespace sealed-secrets \
      --format yaml \
      --scope cluster-wide \
      < applications/argocd/argo-github-repository-credentials-cleartext.yaml \
      > applications/argocd/argo-github-repository-credentials.yaml \
    && rm applications/argocd/argo-github-repository-credentials-cleartext.yaml
    ```

<!-- TODO: more details on this part -->

- Seal the secrets for external-secrets.

- Setup all the secrets in the Gitlab backend with the correct names (search for ExternalSecret resources).

- Commit and push

  ```bash
  git add . && git commit -am "add sealed secrets" && git push
  ```

### ArgoCD installation

- Install ArgoCD with the provided values in your cluster

  ```bash
  cd applications/argocd
  helm dependency build
  kubectl create namespace argocd
  helm template argocd . -n argocd --set argo-cd.server.metrics.serviceMonitor.enabled=false --set argo-cd.redis.metrics.serviceMonitor.enabled=false --set argo-cd.dex.metrics.serviceMonitor.enabled=false --set argo-cd.repoServer.metrics.serviceMonitor.enabled=false --set argo-cd.notifications.metrics.serviceMonitor.enabled=false --set argo-cd.applicationSet.metrics.serviceMonitor.enabled=false   --set argo-cd.controller.metrics.serviceMonitor.enabled=false | kubectl apply -n argocd -f -
  ```

- Apply the secret containing the repository credentials

  ```bash
  kubectl apply -f applications/argocd/argo-github-repository-credentials.yaml -n argocd
  ```

- Apply the app of apps

  ```bash
  kubectl apply -f argo-applications/app-of-apps.yaml -n argocd
  ```

You should be done ! 👌

## 💣 Teardown

- Save all secrets (in `/tmp/kube-secrets`)

  ```bash
  mkdir /tmp/kube-secrets && for ns in $(kubectl ns); do for secret in $(kubectl get secret -n $ns | grep Opaque | awk '{print $1}'); do kubectl get secrets -n $ns $secret -o yaml | kubectl neat > /tmp/kube-secrets/$secret-cleartext.yaml; done; done
  ```

- Teardown the cluster

  ```bash
  sudo kubeadm reset -f
  sudo rm -rf /etc/cni /etc/kubernetes /var/lib/dockershim /var/lib/etcd /var/lib/kubelet /var/run/kubernetes ~/.kube/*
  sudo iptables -F
  sudo iptables -t nat -F
  sudo iptables -t mangle -F
  sudo iptables -X
  ```
