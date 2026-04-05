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

resource "vault_kv_secret_v2" "secrets" {
  for_each = nonsensitive(local.vault_map)

  mount                = vault_mount.kvv2.path
  name                 = each.key
  cas                  = 1
  data_json_wo_version = 1
  delete_all_versions  = true
  data_json_wo         = jsonencode(each.value)
}
