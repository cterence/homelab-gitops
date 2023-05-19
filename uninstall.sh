#!/bin/bash

set -e -u -o pipefail

# Uninstall the Kubernetes cluster, remove the config files, reset iptables and restart docker, kubelet and containerd

ssh -tt terence@homelab "sudo kubeadm reset -f; sudo rm -rf /etc/cni /etc/kubernetes /var/lib/dockershim /var/lib/etcd /var/lib/kubelet /var/run/kubernetes ~/.kube/*; sudo iptables -F; sudo iptables -t nat -F; sudo iptables -t mangle -F; sudo iptables -X; sudo systemctl restart docker kubelet containerd"
