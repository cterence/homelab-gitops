# argo

My Kubernetes cluster state. Managed by ArgoCD. There is a whole lot of stuff here, mostly to experiment.

## Bootstrap

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

- oAuth2-proxy provider

  - Create an application in your desired OAuth2 provider (we will use GitLab as an example) and add https://argocd.yourdomain.com/oauth2/callback as a callback URL

  - Create a temporary secret manifest named `applications/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml`

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      name: gitlab-oauth2-credentials
      namespace: oauth2-proxy
    stringData:
      client-id: _client_id_
      client-secret: _client_secret_
      cookie-secret: _cookie_secret_
    ```

  - Seal the secret

    ```bash
    seal oauth2-proxy gitlab-oauth2-credentials
    # OR
    kubeseal \
      --controller-name=sealed-secrets \
      --controller-namespace sealed-secrets \
      --format yaml \
      < applications/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml \
      > applications/oauth2-proxy/gitlab-oauth2-credentials.yaml \
    && rm applications/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml
    ```

- External DNS

  - Create API keys for your DNS provider (we will use OVH as an example)

  - Create a temporary secret manifest named `applications/external-dns/ovh-credentials-cleartext.yaml`

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      name: ovh-credentials
      namespace: external-dns
    stringData:
      ovh_application_key: _ovh_application_key_
      ovh_application_secret: _ovh_application_secret_
      ovh_consumer_key: _ovh_consumer_key_
    ```

  - Seal the secret

    ```bash
    seal external-dns ovh-credentials
    # OR
    kubeseal \
      --controller-name=sealed-secrets \
      --controller-namespace sealed-secrets \
      --format yaml \
      < applications/external-dns/ovh-credentials-cleartext.yaml \
      > applications/external-dns/ovh-credentials.yaml \
    && rm applications/external-dns/ovh-credentials-cleartext.yaml
    ```

- There are quite a lot of other apps that require sealing a secret, but it's tedious to add them all to this readme, get to work.

- Commit and push

  ```bash
  git add . && git commit -am "add sealed secrets" && git push
  ```

### ArgoCD installation

- Install ArgoCD with the provided values in your cluster

  ```bash
  helm repo add argo https://argoproj.github.io/argo-helm
  helm repo update
  helm install --wait argocd argo/argo-cd --values application/argocd/values.yaml -n argocd --set server.metrics.serviceMonitor.enabled=false --create-namespace=true
  ```

- Apply the secret containing the repository credentials

  ```bash
  kubectl apply -f applications/argocd/argo-github-repository-credentials.yaml -n argocd
  ```

- Apply the app of apps

  ```bash
  kubectl apply -f argo-applications/app-of-apps.yaml -n argocd
  ```

You should be done !

## Teardown

- Save all secrets

  ```bash
  mkdir /tmp/kube-secrets && for ns in $(kns); do for secret in $(k get secret -n $ns | grep Opaque | awk '{print $1}'); do k get secrets -n $ns $secret -o yaml | k neat > /tmp/kube-secrets/$secret-cleartext.yaml; done; done
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
