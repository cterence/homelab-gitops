terraform {
  required_version = "~>1.14"

  required_providers {
    sops = {
      source  = "carlpett/sops"
      version = "1.4.1"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "5.8.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.0.1"
    }
  }
}

provider "sops" {}

provider "vault" {
  address = "http://localhost:8200"
}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "k8s-tailscale-operator.snow-delta.ts.net"
}
