#!/bin/bash

set -e -u -o pipefail

# Get an argument named gitlab token from the command line
# and assign it to the variable GITLAB_TOKEN
GITLAB_TOKEN=$1

kubectl create ns external-secrets --dry-run=client -o yaml | kubectl apply -f -
kubectl create ns argocd --dry-run=client -o yaml | kubectl apply -f -

# Create a kubernetes secret manifest file and apply it on the fly with the GITLAB_TOKEN variable as a value of the token key
cat <<EOF | kubectl apply -f -
apiVersion: v1
kind: Secret
metadata:
  name: gitlab-token
  namespace: external-secrets
type: Opaque
stringData:
  token: $GITLAB_TOKEN
EOF

# Install the external secrets operator from the chart in the applications directory
cd applications/external-secrets
helm dependency build
helm template external-secrets --namespace external-secrets . | kubectl apply -f - 

# Install argocd from the chart in the applications directory
cd ../argocd
helm dependency build
helm template argocd  --namespace argocd . | kubectl apply -f - 

# Install the app of apps
cd ../../argocd-config
kubectl apply --namespace argocd -f app-of-apps.yaml
