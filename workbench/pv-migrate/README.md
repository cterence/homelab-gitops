# pv-migrate

A CLI wrapper over [pv-migrate](https://github.com/utkuozdemir/pv-migrate)
that migrates data from a Released PersistentVolume to a destination PVC.
It creates a temporary PVC that statically binds the source PV, runs
pv-migrate to copy the data, then cleans up the temporary PVC — leaving
the source PV intact (under a Retain reclaim policy) and the destination
PVC populated.

Designed for migrating local-path PVs between nodes in a homelab cluster.

## How it works

1. **Validate** — Reads the source PV, checks it is Released or Available
   (not Bound to a live PVC), and guards against Delete reclaim policies
   unless `--allow-delete-reclaim` is passed.
2. **Clear claimRef** — Patches the PV's `spec.claimRef` to nil so it
   transitions to Available.
3. **Create temp PVC** — Creates a PVC named `pvmig-src-<pv>` with
   `volumeName` set to the source PV, forcing static binding.
4. **Wait for bind** — Polls until the temp PVC is Bound.
5. **Migrate** — Execs the `pv-migrate` CLI, passing the temp PVC as
   source and the destination PVC as dest.
6. **Cleanup** — Deletes the temp PVC. The source PV returns to
   Released (under Retain). Also runs `pv-migrate cleanup --all --force`
   to sweep any Helm releases left behind by an interrupted migration.

Cleanup runs via `defer` with a fresh context, so it fires on success,
failure, or SIGINT.

## Prerequisites

- The `pv-migrate` CLI must be on PATH.
- A kubeconfig (explicit `--kubeconfig`, in-cluster, or `~/.kube/config`).
- The destination PVC must already exist and be in `Pending` state
  (WaitForFirstConsumer) or already bound to a PV on the target node.

## Usage

```bash
# Dry run: show the plan, create nothing
go run . --source-pv pvc-abc123 --dest-pvc my-pvc --dest-namespace my-ns --dry-run

# Same-node migration (both PVs on the same node, default strategies)
go run . --source-pv pvc-abc123 --dest-pvc my-pvc --dest-namespace my-ns

# Cross-node migration (source PV on homelab2, dest PVC on homelab3)
go run . --source-pv pvc-abc123 --dest-pvc my-pvc --dest-namespace my-ns \
  --source-node homelab2 --dest-node homelab3
```

### Flags

| Flag | Description |
|------|-------------|
| `--source-pv` | Name of the source PersistentVolume (required) |
| `--dest-pvc` | Name of the destination PVC (required, must exist) |
| `--dest-namespace` | Namespace of the destination PVC (required) |
| `--temp-namespace` | Namespace for the temp PVC (default: source PV's old claimRef namespace) |
| `--kubeconfig` | Path to kubeconfig (default: in-cluster or ~/.kube/config) |
| `--source-node` | Pin the source-side pod (sshd) to this node via nodeSelector |
| `--dest-node` | Pin the dest-side pod (rsync) to this node via nodeSelector |
| `--strategies` | Comma-separated pv-migrate strategies (default: mount,clusterip,loadbalancer) |
| `--allow-delete-reclaim` | Allow migrating from a PV with Delete reclaim policy |
| `--allow-dest-mismatch` | Skip the check that dest PVC name matches source PV's previous consumer |
| `--delete-extraneous-files` | Pass --dest-delete-extraneous-files to pv-migrate (rsync --delete) |
| `--ignore-mounted` | Do not fail if the source or destination PVC is mounted |
| `--non-root` | Run migration containers as non-root |
| `--no-chown` | Omit chown during rsync |
| `--source-mount-read-write` | Mount the source PVC read-write during migration |
| `--no-compress` | Disable rsync compression |
| `--helm-timeout` | Helm install/uninstall timeout (e.g. 2m) |
| `--log-level` | pv-migrate log level: DEBUG, INFO, WARN, ERROR |
| `--bind-timeout` | How long to wait for the temp PVC to bind (default 5m) |
| `--dry-run` | Print the plan and exit without creating anything |

### Node scheduling

The `--source-node` and `--dest-node` flags use `nodeSelector` (not
`nodeName`) so the Kubernetes scheduler processes the pods. This is
critical for `WaitForFirstConsumer` PVCs — `nodeName` bypasses the
scheduler, which prevents the VolumeBinding plugin from binding the PVC.

When both flags are set and differ, the `mount` strategy is automatically
stripped (it uses a single pod that cannot mount PVs on two different
nodes). The clusterip and loadbalancer strategies use separate pods
(sshd on the source node, rsync on the dest node) connected over a
ClusterIP service.

### Safety checks

- **PV phase** — Refuses if the PV is Bound to a live PVC (delete the PVC
  first so the PV becomes Released). Refuses if the PV is in the Failed
  phase.
- **Reclaim policy** — Refuses if the PV has a Delete reclaim policy
  unless `--allow-delete-reclaim` is passed, because deleting the temp
  PVC would delete the PV and its data.
- **Dest name match** — Checks that the dest PVC name matches the source
  PV's previous consumer (`claimRef.name`). This catches accidental
  mismatches when copying flag values between runs. Bypass with
  `--allow-dest-mismatch`.

## Full migration procedure

```bash
# 1. Disable autosync on the ApplicationSet so generated apps don't self-heal
kubectl patch appset -n argocd applications --type=merge -p \
  '{"spec":{"template":{"spec":{"syncPolicy":{"automated":null}}}}}'

# 2. Disable autosync on the app-of-apps so the ApplicationSet itself doesn't re-sync
kubectl patch application -n argocd app-of-apps --type=merge -p \
  '{"spec":{"syncPolicy":{"automated":null}}}'

# 3. Stop the workload:
#    - Regular app: scale to zero
kubectl scale deploy -n <ns> <app> --replicas=0
#    or for a StatefulSet:
kubectl scale sts -n <ns> <sts> --replicas=0
#    - CNPG cluster: hibernate (cannot scale to zero — webhook rejects instances < 1)
kubectl cnpg hibernate on -n <ns> <cluster>

# 4. For each PVC, note the name and PV name, then delete it
kubectl get pvc -n <ns>
kubectl delete pvc -n <ns> <pvc-name>

# 5. Recreate the PVC:
#    - Regular app: sync only that resource in ArgoCD
#      (a full app sync would scale the workload back up)
argocd app sync <app> --resource :PersistentVolumeClaim:<pvc-name>
#    - CNPG cluster: apply manually WITH owner reference and labels
#      (see "CNPG PVC creation" below)
CLUSTER_UID=$(kubectl get cluster -n <ns> <cluster> -o jsonpath='{.metadata.uid}')
kubectl apply -f - <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: <cluster>-<ordinal>
  namespace: <ns>
  ownerReferences:
    - apiVersion: postgresql.cnpg.io/v1
      kind: Cluster
      name: <cluster>
      uid: ${CLUSTER_UID}
      controller: true
  labels:
    app.kubernetes.io/component: database
    app.kubernetes.io/managed-by: cloudnative-pg
    app.kubernetes.io/name: postgresql
    cnpg.io/cluster: <cluster>
    cnpg.io/instanceName: <cluster>-<ordinal>
    cnpg.io/instanceRole: primary
    cnpg.io/pvcRole: PG_DATA
    role: primary
  annotations:
    cnpg.io/nodeSerial: "<ordinal>"
    cnpg.io/operatorVersion: "1.30.0"
    cnpg.io/pvcStatus: ready
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: <size>
  storageClassName: local-path
EOF

# 6. Run the migration
go run . --source-pv <old-pv-name> --dest-pvc <pvc-name> --dest-namespace <ns> \
  --source-node <old-node> --dest-node <new-node>

# 7. Restart the workload:
#    - Regular app: scale back up
kubectl scale deploy -n <ns> <app> --replicas=1
#    - CNPG cluster: bring back from hibernation, then verify
kubectl cnpg hibernate off -n <ns> <cluster>
kubectl get cluster -n <ns> <cluster>
kubectl get pods -n <ns> -l cnpg.io/cluster=<cluster>

# 8. Re-enable autosync on the ApplicationSet
kubectl patch appset -n argocd applications --type=merge -p \
  '{"spec":{"template":{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}}}'

# 9. Re-enable autosync on the app-of-apps
kubectl patch application -n argocd app-of-apps --type=merge -p \
  '{"spec":{"syncPolicy":{"automated":{"prune":true,"selfHeal":true}}}}'

# 10. Verify, then delete the old PV
kubectl get pv <old-pv-name>  # should be Released
kubectl delete pv <old-pv-name>
```

### Why CNPG needs the owner reference

CNPG discovers a cluster's PVCs via the Kubernetes owner-reference index
(`pvcOwnerKey`), not by label selectors. A manually-created PVC without an
owner reference pointing to the Cluster CR is invisible to the operator.
CNPG sees zero PVCs, sets the cluster to "unrecoverable", and refuses to
create a new primary because the cluster is already initialized. Adding
the owner reference lets CNPG find the PVC, add it to `status.danglingPVC`,
create a pod reattaching to it, and the cluster goes healthy.

### ArgoCD autosync alert

An alert rule `ArgoCDAppOfAppsAutosyncDisabled` is defined in
`k8s-apps/argocd/values.yaml`. It fires when the `app-of-apps` application
has `autosync_enabled="false"` for more than 8 hours, so a disabled
autosync during migration does not go unnoticed.
