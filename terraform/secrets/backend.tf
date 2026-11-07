terraform {
  backend "s3" {
    bucket                      = "terraform-states"
    key                         = "cterence/homelab-gitops/secrets"
    region                      = "us-east-1"
    profile                     = "garage-terraform-states"
    skip_credentials_validation = true
    skip_requesting_account_id  = true
    use_path_style              = true
  }
}
