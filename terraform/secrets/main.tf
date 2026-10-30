data "sops_file" "secrets" {
  source_file = "secrets.yaml"
}

locals {
  # sops keys are "<namespace>.<secret-name>.<key>"
  regex_pattern = "^([^.]+)\\.([^.]+)\\.(.+)$"

  grouped_secrets = {
    for k, v in data.sops_file.secrets.data : k => v
    if can(regex(local.regex_pattern, k))
  }

  secret_map = {
    for path, pairs in {
      for k, v in local.grouped_secrets :
      format("%s/%s", regex(local.regex_pattern, k)[0], regex(local.regex_pattern, k)[1]) => { (regex(local.regex_pattern, k)[2]) = v }...
    } : path => merge(pairs...)
  }

  # Secrets discovered by label scanning instead of name references.
  secret_labels = {
    "argocd/argo-github-repository-credentials" = {
      "argocd.argoproj.io/secret-type" = "repository"
    }
  }

  # data_wo never enters state or plan, so value edits are invisible to
  # terraform: bump this to rewrite every secret.
  data_wo_revision = 2
}

resource "kubernetes_secret_v1" "secrets" {
  for_each = nonsensitive(local.secret_map)

  metadata {
    name      = split("/", each.key)[1]
    namespace = split("/", each.key)[0]
    labels    = try(local.secret_labels[each.key], null)
  }

  data_wo          = each.value
  data_wo_revision = local.data_wo_revision
}
