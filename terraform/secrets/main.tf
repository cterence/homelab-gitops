data "sops_file" "secrets" {
  source_file = "secrets.yaml"
}

locals {
  regex_pattern = "^([^.]+)\\.([^.]+)\\.(.+)$"

  grouped_secrets = {
    for k, v in data.sops_file.secrets.data : k => v
    if can(regex(local.regex_pattern, k))
  }

  vault_map = {
    for k, v in local.grouped_secrets :
    format("%s/%s", regex(local.regex_pattern, k)[0], regex(local.regex_pattern, k)[1]) => {
      (regex(local.regex_pattern, k)[2]) = v
    }...
  }

  final_vault_map = {
    for path, data_list in local.vault_map : path => merge(data_list...)
  }
}

resource "vault_mount" "kvv2" {
  path        = "kvv2"
  type        = "kv"
  options     = { version = "2" }
  description = "KV Version 2 secret engine mount"
}

resource "vault_auth_backend" "kubernetes" {
  type = "kubernetes"
}

resource "kubernetes_cluster_role_binding_v1" "openbao_auth_delegator" {
  metadata {
    name = "openbao-auth-delegator-binding"
  }
  role_ref {
    api_group = "rbac.authorization.k8s.io"
    kind      = "ClusterRole"
    name      = "system:auth-delegator"
  }
  subject {
    kind      = "ServiceAccount"
    name      = "openbao"
    namespace = "openbao"
  }
}

resource "kubernetes_secret_v1" "openbao_auth_token" {
  metadata {
    name      = "openbao-auth-reviewer-token"
    namespace = "openbao"
    annotations = {
      "kubernetes.io/service-account.name" = "openbao"
    }
  }
  type = "kubernetes.io/service-account-token"
}

resource "vault_kubernetes_auth_backend_config" "example" {
  backend                       = vault_auth_backend.kubernetes.path
  kubernetes_host               = "https://kubernetes.default.svc"
  kubernetes_ca_cert            = kubernetes_secret_v1.openbao_auth_token.data["ca.crt"]
  token_reviewer_jwt_wo         = kubernetes_secret_v1.openbao_auth_token.data["token"]
  token_reviewer_jwt_wo_version = 1
  issuer                        = "api"
  disable_iss_validation        = true
}

resource "vault_policy" "external_secrets_policy" {
  name   = "external-secrets-policy"
  policy = <<-EOT
    path "${vault_mount.kvv2.path}/data/*" {
      capabilities = ["read"]
    }

    path "${vault_mount.kvv2.path}/metadata/*" {
      capabilities = ["list", "read"]
    }
    EOT
}

resource "vault_kubernetes_auth_backend_role" "external_secrets" {
  backend                          = vault_auth_backend.kubernetes.path
  role_name                        = "external-secrets"
  bound_service_account_names      = ["external-secrets"]
  bound_service_account_namespaces = ["external-secrets"]
  token_ttl                        = 3600
  token_policies                   = ["default", vault_policy.external_secrets_policy.name]
  audience                         = "openbao"
}

resource "vault_kv_secret_v2" "secrets" {
  for_each = nonsensitive(local.final_vault_map)

  mount                = vault_mount.kvv2.path
  name                 = each.key
  cas                  = 1
  data_json_wo_version = 1
  delete_all_versions  = true
  data_json_wo         = jsonencode(each.value)
}
