# Secret rotation runbook

Secrets live in `secrets.yaml` (SOPS, age keys; agents never decrypt). Keys follow
`<namespace>.<secret-name>.<key>`; each `namespace/secret-name` becomes one k8s
Secret via the generic provisioner in `main.tf` (write-only data, values never in
terraform state). This runbook is organized like the sops file: namespace, then
secret, each entry self-contained.

Every rotation is five steps:

1. Create the new value at the provider (per entry below).
2. Update the key(s): `sops edit secrets.yaml`
3. `terraform apply` (from this directory; S3 backend, bucket
   `terraform-states` — local aws profile/endpoint per your shell setup).
4. Reveal to consumers — the rollout command per secret.
5. Validate, then revoke the old value at the provider.

Notes:

- `data_wo` is write-only: rotation always changes the value, so apply pushes it
  unconditionally. Manual in-cluster edits of a Secret are never detected.
- Consumer types: env-injected (needs pod restart), file-mounted (kubelet updates
  the file, but apps usually read it at start — restart anyway), CronJob pods
  (pick up on next run, no action).
- Reloader auto-restarts controllers annotated `reloader.stakater.com/auto:
  "true"` when their Secret changes (verified: argocd-mcp-server,
  arr-stack-qbittorrent, slskd (music), grafana-mcp-server, paperless-ngx,
  telegram-hub, niks3, registry-main). The manual commands below apply to
  everything else — and work as a fallback for Reloader-covered pods too.
- Check a rotated secret landed:

  ```sh
  kubectl -n <ns> get secret <name> -o jsonpath='{.data.<key>}' | base64 -d   # prints plaintext!
  ```

## argocd

### argo-github-repository-credentials (`url`, `sshPrivateKey`)

A read-only deploy key on cterence/homelab-gitops. ArgoCD reloads repository
secrets live — no restart.

```sh
ssh-keygen -t ed25519 -C "argocd-$(date +%F)" -f /tmp/id_ed25519 -N ''
```

Repo → Settings → Deploy keys → add the public key (read-only). sops:
`sshPrivateKey` = full private key incl. trailing newline, `url` unchanged.
Delete the old deploy key after `argocd app sync` on any app succeeds.

### argo-pocket-id-credentials (`client-secret`)

The ArgoCD OIDC client in Pocket ID (auth.terence.cloud). Client secrets are
single-valued: rotate in Pocket ID, sops, and apply in one sitting; sessions
created with the old secret break. No restart — the settings informer
live-reloads `$secret:key` refs (terraform labels it part-of=argocd).

### argo-webhook-secret (`secret`)

```sh
openssl rand -hex 32
```

Also paste it into the GitHub repo webhook pointing at the argocd webhook
endpoint (repo → Settings → Webhooks). Live-reloaded, no restart.

### argocd-notifications-controller-secret (`telegram-token`)

Unused — `notifications.enabled=false` in `k8s-apps/argocd/values.yaml`, no
controller runs. Do not rotate.

### argocd-mcp-credentials (`api-token`)

An ArgoCD account token: UI (user → API Tokens) or
`argocd account generate-token --account <account>`. The new token works
immediately; revoke the old one after apply:
`argocd account get --account <account>` to find the id, then
`argocd account delete-token --account <account> --id <id>` (deletion is the
only revocation — JWTs carry no expiry unless set with `--expires-in`).

```sh
kubectl -n argocd rollout restart deploy/argocd-mcp-server
```

## arr-stack

### configarr-secrets (`secrets.yml`)

Arr API keys (all *arr apps), collected in one file. The keys rotate in each
app UI (Settings → General → API Key). Consumer is a CronJob — next run picks
the file up. Trigger immediately:

```sh
kubectl -n arr-stack create job --from=cronjob/arr-stack-configarr configarr-manual
```

### gluetun-wg-vm-keys (`WIREGUARD_*`)

sidecar (initContainer, `restartPolicy: Always`) of the arr-stack qbittorrent
pod, via `envFrom`. Only `WIREGUARD_PRIVATE_KEY` rotates —
`WIREGUARD_PUBLIC_KEY`/`ENDPOINT_*` are the server's values (free-oracle-arm-1,
port 51820) and the tunnel IP `192.168.2.2/32` is fixed in
`k8s-apps/arr-stack/values.yaml` (`WIREGUARD_ADDRESSES`). The new public key
must be registered on the WireGuard server before the sidecar reconnects with
the new private key. During the gap, the `WireguardTunnelDown` alert fires
(probe to 192.168.2.1 through the tunnel).

1. Generate the new client keypair (workstation):
   ```sh
   umask 077
   wg genkey | tee client.key | wg pubkey > client.pub
   ```
2. Server (nixos repo, `modules/hosts/free-oracle-arm-1/wireguard.nix`):
   replace the gluetun peer's `publicKey` with the contents of `client.pub`,
   keep `allowedIPs = [ "192.168.2.2/32" ]`, make sure no stale peer shares
   that /32, and rebuild — this kills the old key immediately:
   ```sh
   sudo nixos-rebuild switch --flake .#free-oracle-arm-1
   ```
3. Update the secret and apply — Reloader then restarts the pod by itself:
   ```sh
   sops edit secrets.yaml    # arr-stack.gluetun-wg-vm-keys.WIREGUARD_PRIVATE_KEY
   terraform apply
   kubectl -n arr-stack rollout restart deploy/arr-stack-qbittorrent   # fallback
   ```
4. Verify:
   ```sh
   kubectl -n arr-stack exec deploy/arr-stack-qbittorrent -c gluetun -- wg show
   # latest handshake + probe_success{service="arr-stack-qbittorrent"} == 1
   ```
5. Clean up: `rm client.key client.pub`

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

Verify the next scheduled backup completes and instance logs show WAL
archiving succeeding.

## external-dns

### cloudflare-credentials (`cloudflare_api_token`)

Dashboard → My Profile → API Tokens → Create; DNS Read + DNS Write + Zone
Read on the zone (Zone Read is required for zone-id discovery; Zone Write is
not needed — leave it off). Old and new tokens coexist — revoke the old after
verify.

If the old token is not in My Profile → API Tokens, check the account-level
page (dashboard → account → Manage API Tokens) or another member's tokens.
Identify by value and revoke by id:

```sh
OLD=<old token>   # from sops
curl -s -H "Authorization: Bearer $OLD" \
  https://api.cloudflare.com/client/v4/user/tokens/verify | jq '.result'   # -> id
# temp token with User → API Tokens → Edit, revoke right after:
curl -X DELETE -H "Authorization: Bearer $ADMIN" \
  https://api.cloudflare.com/client/v4/user/tokens/<id>
curl -s -H "Authorization: Bearer $OLD" \
  https://api.cloudflare.com/client/v4/user/tokens/verify | jq '.result.status'   # invalid
```

```sh
kubectl -n external-dns rollout restart deploy/external-dns
kubectl -n external-dns logs deploy/external-dns --tail=20   # no auth errors
```

## falco

### falcosidekick-telegram (`TELEGRAM_TOKEN`, `TELEGRAM_CHATID`)

BotFather → /mybots → bot → API Token → Revoke. Killing the old token is
immediate — provider + sops + apply + restart in one sitting (`TELEGRAM_CHATID`
stays the same):

```sh
kubectl -n falco rollout restart deploy/falco-falcosidekick
```

### ghcr-dockerconfig (`config.json`)

GitHub PAT with `read:packages` (Settings → Developer settings). File:
`{"auths":{"ghcr.io":{"auth":"<base64 user:token>"}}}`. Read only by the
falcoctl init container, at pod start:

```sh
kubectl -n falco rollout restart ds/falco
```

## immich

### immich-pocket-id-client-secret (`client-secret`)

The immich OIDC client in Pocket ID. Single-valued: rotate in Pocket ID, sops,
apply in one sitting (a brief login break).

```sh
kubectl -n immich rollout restart deploy/immich-server   # config template env
```

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow --read --write cnpg-backups
--key cnpg-backups` (garage section), sops + terraform apply. No CNPG restart
needed — the barman-cloud plugin sidecar reads the ObjectStore and secret live.
Verify:

```sh
kubectl -n immich exec <pod> -c postgres -- psql -X -tA -c \
  "select last_archived_time, last_failed_time from pg_stat_archiver"
```

## monitoring

### grafana-mcp-credentials (`sa-token`)

Grafana UI → Administration → Service accounts → new token. Local MCP clients
(vibe/claude connectors) hold the same token — update the connector config and
restart the MCP session too.

```sh
kubectl -n monitoring rollout restart deploy/grafana-mcp-server
```

### pocket-id-credentials (`GF_AUTH_GENERIC_OAUTH_CLIENT_SECRET`)

The Grafana OIDC client in Pocket ID. Single-valued: rotate, sops, apply in one
sitting; Grafana users re-login.

```sh
kubectl -n monitoring rollout restart deploy/kube-prometheus-stack-grafana
```

### telegram-bot-token (`token`, `chat-id`)

BotFather → /mybots → bot → API Token → Revoke (`chat-id` stays). Alertmanager
reads it from mounted `*_file` paths:

```sh
kubectl -n monitoring rollout restart sts/alertmanager-kube-prometheus-stack-alertmanager
```

## music

### slskd-credentials (`SLSKD_SLSK_USERNAME`, `SLSKD_SLSK_PASSWORD`)

Soulseek has no password change/reset — the account is the username+password
pair registered on first login. "Rotating" means a new account: put a new
username+password in the sops keys (connecting registers it); privileges and
peer trust do not transfer. Same username requires the account to lapse 30
days (no privileges) or admin help on the slsknet forums.

```sh
sops edit secrets.yaml    # music.slskd-credentials.SLSKD_SLSK_USERNAME/PASSWORD
terraform apply           # Reloader restarts the slskd deployment automatically
kubectl -n music logs deploy/slskd   # no invalid-credentials loop
```

## niks3

### api-token (`token`)

Self-issued bearer token for niks3 clients: `openssl rand -hex 32`. Update
every client that pushes to the cache (GitHub Actions `nix-cache` workflow
secret, local CLI config) — that wiring lives outside this repo.

```sh
kubectl -n niks3 rollout restart deploy/niks3
```

### signing-key (`signing-key`)

Nix cache signing keypair:

```sh
nix-store --generate-binary-cache-key niks3.terence.cloud-1 cache-key.sec cache-key.pub
```

sops gets the secret half; the public half must be added to every consumer's
trusted substituters config (nixos configs, flake `nixConfig`). Keep the old
public key trusted until the cache is rebuilt — old artifacts stay signed with it.

```sh
kubectl -n niks3 rollout restart deploy/niks3
```

### s3-credentials (`access-key`, `secret-key`)

The niks3 backing store key (bucket `nix-cache` on garage). Rotate via
`/garage key create nix-cache` + `bucket allow` (garage section), sops +
terraform apply; niks3 re-reads the secret (Reloader restarts it):

```sh
kubectl -n niks3 rollout restart deploy/niks3
```

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

## paperless-ngx

### paperless-ngx-socialaccount-providers (`socialaccount-providers`)

The Pocket ID client secret sits inside the JSON blob — rebuild the whole JSON
(sops: `paperless-ngx.socialaccount-providers.socialaccount-providers`).

```sh
kubectl -n paperless-ngx rollout restart deploy/paperless-ngx
```

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

## pocket-id

### pocket-id-encryption-key (`ENCRYPTION_KEY`)

Rotatable in place (pocket-id v2.19+): the token signing keys and other
sensitive DB fields are RE-encrypted, not regenerated — client credentials
(ArgoCD, Grafana, immich, paperless) survive; nobody re-logs-in. Do all steps
in one sitting: between re-encryption and restart, the pod still holds the old
key while the DB uses the new one.

```sh
NEW=$(openssl rand -base64 32)
kubectl -n pocket-id exec sts/pocket-id -- ./pocket-id encryption-key-rotate --new-key "$NEW" -y
sops edit secrets.yaml    # pocket-id.pocket-id-encryption-key.ENCRYPTION_KEY = $NEW
terraform apply           # then: kubectl -n pocket-id rollout restart sts/pocket-id
```

Verify with `./pocket-id healthcheck` and one login through any consumer.
Do NOT confuse with `key-rotate`: that regenerates the token signing key and
invalidates every active session — an availability event, not a rotation step.

### pocket-id-maxmind-key (`MAXMIND_LICENSE_KEY`)

maxmind.com → Account → Manage License Keys. MaxMind allows a fixed number of
keys; delete the old one.

```sh
kubectl -n pocket-id rollout restart sts/pocket-id
```

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

## lastfm-scrobble-deduplicator

### lastfm-credentials (`LASTFM_PASSWORD`, `LASTFM_USERNAME`)

Password set on last.fm (`LASTFM_USERNAME` is not a secret). CronJob — next run
picks it up.

## tailscale-operator

### operator-oauth (`client_id`, `client_secret`)

Admin console → Settings → OAuth clients → Generate; assign the `tag:k8s` tag,
key expiry off. Both keys rotate together. Running proxies keep working; only
new proxies need the new client.

```sh
kubectl -n tailscale-operator rollout restart deploy/operator
```

## vaultwarden

### vaultwarden-admin-token (`token`)

Zero-risk rotation: this token only gates the `/admin` panel — vault
contents are zero-knowledge encrypted client-side, so no rotation of it can
affect user data, sessions, or logins. The worst case is a temporary /admin
lockout, always recoverable by writing a fresh hash. The chart injects the
PHC string into `ADMIN_TOKEN` (a Plaintext value also works but vaultwarden
logs a warning); store the real password in the password manager.

```sh
pw=$(openssl rand -base64 24); echo "store in password manager: $pw"
printf '%s' "$pw" | argon2 "$(openssl rand -base64 16)" -e -id   # PHC string
sops edit secrets.yaml    # vaultwarden.vaultwarden-admin-token.token = $argon2id$...
terraform apply
kubectl -n vaultwarden rollout restart deploy/vaultwarden   # no Reloader annotation
```

Verify: log into /admin with the plaintext (admin-panel cookies are
invalidated by the rotation). No ordering trap here, unlike pocket-id: the
stored hash is the server's only input, so the steps can't desync.

ARGON2 GOTCHA — `/data/config.json` on the PVC overrides `ADMIN_TOKEN` env.
Vaultwarden writes that file whenever settings are saved in the /admin panel,
snapshotting the admin_token hash at that moment; while the key exists there,
sops rotations of this entry are silently ignored (startup logs list which
env vars are overridden — check ADMIN_TOKEN's absence after restarts). If a
rotation "doesn't take": delete the key from `/data/config.json` (keep a
backup copy), restart, and the env/sops value becomes authoritative:

```sh
kubectl -n vaultwarden exec deploy/vaultwarden -- sh -c \
  'cp /data/config.json /data/config.json.bak && sed -i "/\"admin_token\":/d" /data/config.json'
kubectl -n vaultwarden rollout restart deploy/vaultwarden
# then check startup logs: ADMIN_TOKEN must NOT be in the overridden list
```

Generate the hash on NixOS with `nix shell nixpkgs#libargon2.out` (provides
the `argon2` CLI); a mangled PHC string fails the /admin login while the
logs only say `Invalid admin token`.

### vaultwarden-installation (`PUSH_INSTALLATION_ID`, `PUSH_INSTALLATION_KEY`)

Unused — chart has `pushNotifications: {}`. If enabling push, register at
bitwarden.com/host first.

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

## velero

### backblaze-credentials (`config`)

B2 → App Keys → Add Application Key, restricted to the velero backup bucket.
ini body of the `config` key:

```ini
[default]
aws_access_key_id=<keyID>
aws_secret_access_key=<appKey>
```

Old and new coexist; delete the old in B2 after a successful backup.

```sh
kubectl -n velero rollout restart deploy/velero ds/node-agent
kubectl -n velero get backup   # next scheduled backup must complete
```

### velero-repo-credentials (`repository-password`)

Kopia repo password — global, one secret feeds every BSL's repos. There is
no in-place rotation: a fresh repo under a new prefix is the mechanism. Keep
the OLD password in the password manager; it is the only key to all
pre-rotation backups (and their deletion).

1. `NEW=$(openssl rand -base64 24)` — store old + new in the password manager.
2. sops: `velero.velero-repo-credentials.repository-password` = new;
   terraform apply. Restart `deploy/velero ds/node-agent`.
3. In `k8s-apps/velero/values.yaml` add a NEW BSL for the new generation
   (e.g. `garage-v2`, prefix `v2`, credentials as for garage) and demote the
   current default to `default: false`. Do NOT mutate the old BSL's prefix —
   the old VeleroRepository CRs point into the old prefix with the old
   password; a fresh BSL gives them a clean break.
4. First backup per schedule initializes per-namespace repos under the new
   prefix, encrypted with the new password.

Rotate = read-only history: pre-rotation restores need the OLD password
temporarily applied to the secret (sops/apply, restore, flip forward). Their
TTL expiry ALSO needs repo access — it will fail silently after the rotation,
so once the retention window passes, purge manually: `velero backup delete` +
delete the old prefix objects via rclone. Repositories on decommissioned
storage become unrestorable when that storage goes away.

### garage-credentials (`config`)

ini file (format as above) for velero's garage BSL (`s3://velero/<prefix>`,
endpoint `http://garage.garage.svc:3900`). Rotate via
`/garage key create velero` + `bucket allow` (garage section), sops +
terraform apply, then:

```sh
kubectl -n velero rollout restart deploy/velero ds/node-agent
```

## garage

### garage-rpc-secret (`rpcSecret`)

The gateways' cluster auth secret (env-injected by the terraform-created
`garage-rpc-secret`). Also the admin credential: the `garage` CLI authenticates
over RPC with it, and it is already in the pod env — CLI calls from inside the
pod need no credentials at all. No S3-side effect; rotate = sops + terraform
apply (Reloader restarts the StatefulSet).

```sh
kubectl -n garage exec sts/garage -c garage -- /garage status
```

### S3 keys (`cnpg-backups`, `velero`, `nix-cache`, `terraform-states`, `obsidian`)

One key per bucket, created in-pod with the CLI. Garage keys have no secret
rotation command — "rotating" means: create the replacement, `bucket allow`
it, sops + terraform apply, re-point consumers, delete the old key and its
permissions:

```sh
kubectl -n garage exec sts/garage -c garage -- /garage key create cnpg-backups
# prints access + secret ONCE — straight into the sops entries
kubectl -n garage exec sts/garage -c garage -- /garage bucket allow \
  --read --write cnpg-backups --key cnpg-backups
```

Remote CLI from a laptop: `kubectl -n garage port-forward sts/garage 3901:3901`
then `garage --rpc-host <full-node-id>@localhost:3901 -s "$RPC_SECRET" status`
(get the node id with `/garage node id`; RPC is port 3901, the admin HTTP API
is 3903 and never used by the CLI).

### Verifying CNPG WAL archiving

The barman-cloud plugin sidecar reads the ObjectStore and the credentials
secret live from the API — no pod or DB restart is needed after rotating the
pair or changing the endpoint. Verify per cluster:

```sh
kubectl -n <ns> exec <cluster-pod> -c postgres -- psql -X -c \
  "select archived_count, last_archived_wal, last_archived_time, failed_count, last_failed_time from pg_stat_archiver"
# pending (unarchived) segments — CNPG pods have no shell, use SQL:
kubectl -n <ns> exec <cluster-pod> -c postgres -- psql -X -c \
  "select count(*) from pg_ls_dir('pg_wal/archive_status') where pg_ls_dir like '%.ready'"
```

A failed archive retries when a new WAL segment closes; `pg_switch_wal()`
forces one immediately (safe, verification-only).

### Gotcha: ObjectStore changes do not auto-apply

The CNPG cluster chart renders the per-cluster `ObjectStore` CR (endpoint,
destination, credentials reference) as a Helm hook (`pre-install,pre-upgrade`).
ArgoCD excludes hook resources from the sync diff — a commit that only
changes a `backups:` stanza leaves the app "Synced" and **never applies the
new ObjectStore**. After any change to the CNPG backup configuration, force
the sync so the PreSync hook runs:

```sh
kubectl -n argocd annotate application <app> argocd.argoproj.io/refresh=hard --overwrite
# that alone is not enough for hook-only changes — hit Sync in the UI or:
argocd app sync <app>
```

A drift symptom of having missed this: WAL archives fail with barman
`exit status 4` while the ObjectStore still shows the old `spec.configuration.endpointURL`.

## telegram-hub

### credentials (`ALLOWED_USERS`, `MISTRAL_API_KEY`, `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID`)

`TELEGRAM_BOT_TOKEN`: BotFather → /mybots → revoke, rotate in one sitting.
`MISTRAL_API_KEY`: console.mistral.ai → API Keys. `ALLOWED_USERS` and
`TELEGRAM_CHAT_ID` are ids, not secrets.

```sh
kubectl -n telegram-hub rollout restart deploy/telegram-hub
```

## cert-manager

### cloudflare-api-token (`cloudflare_api_token`)

Dashboard → My Profile → API Tokens → Create; DNS Read + DNS Write + Zone
Read on the zone (dns-01 only; same scope rules as external-dns). Old and new
coexist — revoke the old after verify. Used by both ClusterIssuers, read per
challenge — no restart. Verify on the next issuance
(`kubectl get certificaterequest -A`).

## home-assistant

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

## gitea

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Keys are the shared garage `cnpg-backups` S3 key pair (all nine namespaces use
it; all nine sops entries must change together). Rotate via
`/garage key create cnpg-backups` + `bucket allow` (garage section), sops +
terraform apply. No CNPG restart needed — the barman-cloud plugin sidecar
reads the ObjectStore and secret live. Verify with the pg_stat_archiver query
(garage section).

## cnpg-restore-test

### cnpg-backup-s3-creds (`ACCESS_KEY_ID`, `ACCESS_SECRET_KEY`)

Restore-test credentials use the shared garage `cnpg-backups` pair — rotate
it together with the other nine. No live workload — the next restore test picks the new pair up.
The first post-migration restore test against garage is the end-to-end proof
of the whole cutover.

## registry

### registry-auth (`password`, `htpasswd`)

Password + matching bcrypt htpasswd, regenerate both together (registry reads
the htpasswd file; username is `registry`):

```sh
pw=$(openssl rand -base64 24); echo "same value as password key: $pw"
htpasswd -Bbn registry "$pw"    # output line goes into the htpasswd key
kubectl -n registry rollout restart deploy/registry-main
```

The pull secret fans out via the `registry-pull-secret` ClusterExternalSecret
(`refreshTime: 1h`) to every namespace listed in `k8s-apps/registry/values.yaml`.
New pulls use the new password once the ExternalSecrets refresh; Kaniko jobs
(`k8s-apps/workbench/templates/job.yaml`) read `registry-auth` directly and
pick it up on the next build:

```sh
kubectl get clusterexternalsecret registry-pull-secret -o jsonpath='{.status}'
```
