terraform {
  required_version = "~>1.14"

  required_providers {
    sops = {
      source  = "carlpett/sops"
      version = "1.4.1"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.4.0"
    }
  }
}

provider "sops" {}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "k8s-tailscale-operator.snow-delta.ts.net"
}
