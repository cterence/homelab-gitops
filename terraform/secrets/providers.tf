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
  }
}

provider "sops" {}

provider "vault" {
  address = "http://localhost:8200"
}
