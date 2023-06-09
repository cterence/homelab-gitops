#!/bin/bash

set -e -u -o pipefail

# Get an argument named gitlab token from the command line
# and assign it to the variable GITLAB_TOKEN
GITLAB_TOKEN=$1

ssh -tt terence@homelab "sudo kubeadm init --apiserver-cert-extra-sans homelab --pod-network-cidr 10.244.0.0/16; mkdir -p $HOME/.kube; sudo cp -i /etc/kubernetes/admin.conf $HOME/.kube/config;   sudo chown $(id -u):$(id -g) $HOME/.kube/config"

scp -q terence@homelab:/home/terence/.kube/config ~/.kube/config || true

sed -i 's/192.168.2.64/homelab/g' ~/.kube/config

kubectl apply -f https://raw.githubusercontent.com/coreos/flannel/master/Documentation/kube-flannel.yml

kubectl taint nodes homelab node-role.kubernetes.io/control-plane:NoSchedule-

kubectl create ns external-secrets --dry-run=client -o yaml | kubectl apply -f -
kubectl create ns argocd --dry-run=client -o yaml | kubectl apply -f -

# Create a kubernetes secret manifest file and apply it on the fly with the GITLAB_TOKEN variable as a value of the token key
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: gitlab-secret
  namespace: external-secrets
type: Opaque
stringData:
  token: $GITLAB_TOKEN
EOF

# Install the external secrets operator from the chart in the applications directory
cd k8s-apps/external-secrets
helm dependency update
helm template external-secrets --namespace external-secrets . | kubectl apply --namespace external-secrets -f - 

# Install argocd from the chart in the applications directory
cd ../argocd
helm dependency update
helm template argocd  --namespace argocd . | kubectl apply --namespace argocd -f - 

# Install the app of apps
cd ../../argocd-apps/
kubectl apply --namespace argocd -f app-of-apps.yaml

rm ~/.kube/config
tailscale configure kubeconfig k8s-tailscale-operator
