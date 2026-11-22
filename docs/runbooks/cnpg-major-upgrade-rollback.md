# Runbook: Repairing a failed in-place CNPG postgres major upgrade

## Background

Clusters managed by CloudNativePG pick their image from an
ImageCatalog: bumping the `postgresql` major makes CNPG run a major
upgrade Job that `pg_upgrade`s the data into a new `pgdata-new`
directory and then swaps the instance image.

If that Job fails, the instance pod restarts on the new image against
the old (not-upgraded) data directory and stays down until the cluster
status is rolled back by hand.

## Fix

1. Delete the (failed) major upgrade Job in the cluster's namespace.
2. The instance pod will restart on the new postgres image — it must
   be rolled back.
3. Patch the Cluster status back to the previous image (from the
   ImageCatalog entry of the old major):
   ```sh
   kubectl patch cluster <cluster> -n <namespace> \
     --type merge --subresource status \
     -p '{"status":{"image":"_previous_image_","phase":"Healthy"}}'
   ```
4. Delete the instance pod and let the cluster recreate it. It should
   come back up normally, as if nothing had happened.
5. Inside the pod's data volume, delete the leftover `pgdata-new`
   directory from the upgrade attempt.

## Gotchas

- Roll the `postgresql` major back in the app's values too, or ArgoCD
  re-applies the failed upgrade.
- The previous image is the `image:` field of the old major in the
  ImageCatalog, not a tag from memory.
- Deleting `pgdata-new` matters: a stale half-written directory makes
  the next real upgrade attempt fail or reuse garbage.
