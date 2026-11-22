# Runbook: Repairing a looping Velero-Kopia maintenance failure

## Symptoms

- Loki alert `VeleroRepoMaintenanceFailed`; velero log
  `Pruning repository failed` with `error="<nil>"` (a cosmetic velero
  bug — the real error is elsewhere).
- Check the actual error:
  ```sh
  kubectl get backuprepository -n velero <repo> \
    -o jsonpath='{.status.recentMaintenance}'
  ```

## Background

If the message is `error rewriting contents in short packs: failed to
rewrite N contents`, the kopia index references pack blobs that no
longer exist in B2 (an interrupted backup session). Full maintenance
fails at the rewrite step and aborts before the step that purges the
index entries, so the loop repeats daily (failure, then a successful
retry of nothing useful).

The affected contents are already marked `deleted` (garbage from an
aborted session, never restorable) — they can be purged without data
loss.

## Fix

1. Get the B2 credentials (note: values in the secret are quoted,
   strip the quotes):
   ```sh
   export AWS_ACCESS_KEY_ID="$(kubectl get secret -n velero backblaze-credentials -o jsonpath='{.data.config}' | base64 -d | awk '/aws_access_key_id/ {print $3}' | tr -d '"')"
   export AWS_SECRET_ACCESS_KEY="$(kubectl get secret -n velero backblaze-credentials -o jsonpath='{.data.config}' | base64 -d | awk '/aws_secret_access_key/ {print $3}' | tr -d '"')"
   ```
2. Connect kopia to the repository. Repo name is
   `<namespace>-<bsl>-kopia` (e.g. `versity-gw-v2-kopia` = namespace
   `versity-gw` on BSL `v2` → prefix `v2/kopia/versity-gw/`). The
   user/hostname override is mandatory, otherwise kopia rejects the
   run with `maintenance must be run by designated user: default@default`:
   ```sh
   kopia repo connect s3 \
     --bucket=velero-backup-tch \
     --prefix=v2/kopia/<ns>/ \
     --endpoint=s3.eu-central-003.backblazeb2.com \
     --override-username=default \
     --override-hostname=default \
     --password="$(kubectl get secret -n velero velero-repo-credentials -o jsonpath='{.data.repository-password}' | base64 -d)"
   ```
3. Run maintenance with the magic variable (it ignores rewrite
   failures for `deleted` contents only, letting maintenance reach the
   purge step and complete):
   ```sh
   KOPIA_IGNORE_MAINTENANCE_REWRITE_ERROR=1 kopia maintenance run --full --force
   ```
4. Verify the ghost entries are purged from the index:
   ```sh
   kopia content list --deleted | grep <content-id>
   ```
5. Disconnect and restart the velero cycle:
   ```sh
   kopia repo disconnect
   kubectl patch backuprepository -n velero <repo> \
     --type merge -p '{"status":{"lastMaintenanceTime":null}}'
   ```
6. If nothing happens within ~10 min, the velero reconcile is
   probably stuck: restart the velero pod.
7. Check for a recent `Succeeded` in `recentMaintenance` — a ~10s
   quick maintenance suffices, the full cycle has already run.

## Gotchas

- Do not use `kopia index recover --delete-indexes` for this: it only
  removes V0 index blobs (`n*`), this repo uses V1 indexes (`xn*`) →
  it deletes nothing and only adds duplicates.
- Per-content errors are in `~/Library/Logs/kopia/content-logs/` (not
  in console output or cli-logs).
- Make sure no velero backup window overlaps the operation (v2
  schedule: ~04:10, maintenance ~15:09 UTC).
- Do not touch the Loki alert: it correctly detected the incident (one
  line per day is enough to trigger it, by design).
