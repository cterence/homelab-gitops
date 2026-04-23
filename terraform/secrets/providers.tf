terraform {
  required_version = "~>1.14"

  required_providers {
    sops = {
      source  = "carlpett/sops"
      version = "1.4.1"
    }
    vault = {
      source  = "hashicorp/vault"
      version = "5.9.0"
    }
    kubernetes = {
      source  = "hashicorp/kubernetes"
      version = "3.1.0"
    }
  }
}

provider "sops" {}

provider "vault" {
  address = "http://openbao.snow-delta.ts.net:8200"
}

provider "kubernetes" {
  config_path    = "~/.kube/config"
  config_context = "k8s-tailscale-operator.snow-delta.ts.net"
}
