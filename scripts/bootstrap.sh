#!/bin/bash

set -e -u -o pipefail

# Get an argument named gitlab token from the command line
# and assign it to the variable GITLAB_TOKEN
GITLAB_TOKEN=$1

ssh -tt terence@homelab "cd ~/homelab-gitops; sudo k0s install controller --enable-worker -c ./k0s.yaml; sudo k0s start; sleep 5; sudo k0s status; sudo k0s kubeconfig admin > ~/.kube/config"

scp -q terence@homelab:/home/terence/.kube/config ~/.kube/config || true

kubectl taint nodes --all node-role.kubernetes.io/master-

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

cd k8s-apps/cilium && helm dependency update && helm template cilium . -n kube-system | kubectl apply -n kube-system -f -

# Install the external secrets operator from the chart in the applications directory
cd ../external-secrets
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
