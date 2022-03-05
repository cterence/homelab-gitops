# argo

A cool repo to deploy stuff to a Kubernetes cluster with ArgoCD.

This is a WIP as I'm in the process of moving my code to GitHub, it's still linked to my GitLab project. As of now, this is just a showcase.

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

- Add the flannel CNI

  ```bash
  kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml
  ```

- Remove the master node taint (if you are running a single node cluster)

  ```bash
  kubectl taint nodes <node_name> node-role.kubernetes.io/master:NoSchedule-
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
      < manifests/"$1"/"$2"-cleartext.yaml \
      > manifests/"$1"/"$2".yaml \
      && rm manifests/"$1"/"$2"-cleartext.yaml
  }
  ```

- **Repository credentials**

  - Generate an ssh key

    ```bash
    ssh-keygen -t ed25519 -C "ArgoCD" -f argocd -N ""
    ```

  - Add the public ssh key to your git remote repository

  - Create a temporary secret manifest named `manifests/argocd/argo-repository-credentials-cleartext.yaml` from the ssh private key

    ```yaml
    apiVersion: v1
    kind: Secret
    metadata:
      name: argo-repository-credentials
      namespace: argocd
      labels:
        argocd.argoproj.io/secret-type: repository
    stringData:
      sshPrivateKey: |
        -----BEGIN OPENSSH PRIVATE KEY-----
        _key_
        -----END OPENSSH PRIVATE KEY-----
      url: _your_repo_ssh_uri_
    ```

  - Delete the ssh keys

    ```bash
    rm argocd argocd.pub
    ```

  - Seal the secret

    ```bash
    seal argocd argo-repository-credentials
    # OR
    kubeseal \
      --controller-name=sealed-secrets \
      --controller-namespace sealed-secrets \
      --format yaml \
      --scope cluster-wide \
      < manifests/argocd/argo-repository-credentials-cleartext.yaml \
      > manifests/argocd/argo-repository-credentials.yaml \
    && rm manifests/argocd/argo-repository-credentials-cleartext.yaml
    ```

- OAuth2 Provider

  - Create an application in your desired OAuth2 provider (we will use GitLab as an example) and add https://argocd.yourdomain.com/oauth2/callback as a callback URL

  - Create a temporary secret manifest named `manifests/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml` from the ssh private key

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
      < manifests/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml \
      > manifests/oauth2-proxy/gitlab-oauth2-credentials.yaml \
    && rm manifests/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml
    ```

- External DNS

  - Create API keys for your DNS provider (we will use OVH as an example)

  - Create a temporary secret manifest named `manifests/external-dns/ovh-credentials-cleartext.yaml` from the ssh private key

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
      < manifests/external-dns/ovh-credentials-cleartext.yaml \
      > manifests/external-dns/ovh-credentials.yaml \
    && rm manifests/external-dns/ovh-credentials-cleartext.yaml
    ```

- Commit and push

  ```bash
  git add . && git commit -am "add sealed secrets" && git push
  ```

### ArgoCD installation

- Install ArgoCD with the provided values in your cluster

  ```bash
  helm repo add argo https://argoproj.github.io/argo-helm
  helm repo update
  helm install --wait argocd argo/argo-cd --values helm-values/argocd.yaml -n argocd --set server.metrics.serviceMonitor.enabled=false --create-namespace=true
  ```

- Apply the secret containing the repository credentials

  ```bash
  kubectl apply -f manifests/argocd/argo-repository-credentials.yaml -n argocd
  ```

- Apply the app of apps

  ```bash
  kubectl apply -f argo-applications/app-of-apps.yaml -n argocd
  ```

You should be done !

## Teardown

- Save important secrets before teardown :

  ```bash
  kubectl get secret argo-repository-credentials -n argocd -o yaml | kubectl neat > manifests/argocd/argo-repository-credentials-cleartext.yaml
  kubectl get secret ovh-credentials -n external-dns -o yaml | kubectl neat > manifests/external-dns/ovh-credentials-cleartext.yaml
  kubectl get secret gitlab-oauth2-credentials -n oauth2-proxy -o yaml | kubectl neat > manifests/oauth2-proxy/gitlab-oauth2-credentials-cleartext.yaml
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
