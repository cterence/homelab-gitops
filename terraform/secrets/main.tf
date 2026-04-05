data "sops_file" "secrets" {
  source_file = "secrets.yaml"
}

locals {
  split_keys = [for k in keys(data.sops_file.secrets.data) : split(".", k)]

  grouped_secrets = {
    for k0, vals0 in { for k in local.split_keys : k[0] => k... } : k0 => {
      for k1, vals1 in { for v in vals0 : v[1] => v... } : k1 => {
        for v in vals1 : v[2] => data.sops_file.secrets.data[join(".", v)]
      }
    }
  }

  vault_map = merge([
    for ns, secrets in local.grouped_secrets : {
      for s_name, s_data in secrets : "${ns}/${s_name}" => s_data
    }
  ]...)
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
  for_each = nonsensitive(local.vault_map)

  mount                = vault_mount.kvv2.path
  name                 = each.key
  cas                  = 1
  data_json_wo_version = 1
  delete_all_versions  = true
  data_json_wo         = jsonencode(each.value)
}
