# Morpheus CSI

Experimental CSI driver for provisioning Morpheus-backed storage in Kubernetes.

This repository is still in MVP territory. The controller can drive Morpheus
server resize operations to create data disks, and the Kubernetes deployment
manifests are usable for lab testing. Node-side mounting is still being refined,
especially around discovering the Linux device path after Morpheus attaches a
new disk.

## Current State

- Kubernetes manifests live under `deploy/kubernetes/` and are organized as a
  Kustomize base plus an example overlay.
- The image is published by GitHub Actions to
  `ghcr.io/da3m0nsec/morpheus-csi:main`.
- The controller implements CSI identity, create/delete, publish/unpublish, and
  expand entry points.
- `CreateVolume` uses `PUT /api/servers/{serverId}/resize` and sends the full
  desired Morpheus `volumes` array, preserving existing disks and adding new CSI
  volumes with `id: -1`.
- `StorageClass` selects the Morpheus server, datastore, and storage type. The
  server ID is currently the initial provisioning server.
- Controller publish resolves the target Kubernetes node from the
  `morpheus.csi/server-id` node label when present. If the label is missing, it
  queries Morpheus servers with the Kubernetes node name and uses an unambiguous
  match.
- Moving an existing CSI volume between Morpheus servers uses the HVM-only
  server volume detach/attach endpoints:
  `PUT /api/servers/{id}/volumes/{volumeId}/detach` and
  `PUT /api/servers/{id}/volumes/{volumeId}/attach`.
- The node plugin runs privileged and performs repeated SCSI host rescans before
  staging a volume, because newly attached VM disks may not appear in `lsblk`
  immediately after Morpheus attaches them.
- When Morpheus does not return a Linux device path, the node plugin discovers
  the attached disk by scanning for an unmounted, unpartitioned, size-matching
  block device. It prefers a blank (unformatted) disk for a newly created
  volume, and falls back to a single already-formatted disk of the requested
  size when an existing volume is re-attached to the node (for example after a
  pod reschedule). Ambiguous matches are rejected rather than guessed.

## Known Limitations

- Workers can be labeled with `morpheus.csi/server-id=<morpheus-server-id>` to
  override automatic node-name lookup or disambiguate multiple Morpheus matches.
- Existing volume detach/attach is available for HVM only in Morpheus.
- New volume creation still uses server resize with `id: -1`; the attach
  endpoint only applies to existing volumes.
- Cross-node movement still needs live validation against the target
  Morpheus/vSphere environment.
- Snapshots, clones, raw block volumes, topology, health monitoring, and Windows
  nodes are out of scope for the MVP.
- The Morpheus API behavior around resize, delete, and returned volume metadata
  is still being validated against a live environment.

## Deploy

Create the editable secret file:

```sh
cp deploy/kubernetes/overlays/example/secret.env.example deploy/kubernetes/overlays/example/secret.env
```

Edit:

- `deploy/kubernetes/overlays/example/secret.env`
- `deploy/kubernetes/overlays/example/storageclass.yaml`

Then deploy:

```sh
kubectl apply -k deploy/kubernetes/overlays/example
kubectl -n morpheus-csi get pods
```

The most important values are:

- `MORPHEUS_URL`
- `MORPHEUS_TOKEN`
- `MORPHEUS_INSECURE_SKIP_VERIFY`, useful for labs with self-signed certs
- `MORPHEUS_DEBUG`, optional API request/response logging for troubleshooting
- `morpheus.serverId`, the initial Morpheus server used for provisioning
- `morpheus.storageType`, default example `38`
- `morpheus.datastoreId`

If Morpheus server names/hostnames match Kubernetes node names, the driver can
resolve nodes automatically. Otherwise, label the Kubernetes worker nodes with
their Morpheus server IDs:

```sh
kubectl label node k8s-test-1-worker-1 morpheus.csi/server-id=895
kubectl label node k8s-test-1-worker-2 morpheus.csi/server-id=896
```

## Test PVC

Create the test claim and pod:

```sh
kubectl apply -f deploy/kubernetes/examples/pvc.yaml
kubectl apply -f deploy/kubernetes/examples/testpod.yaml
```

When a pod using the PVC is deleted, Kubernetes calls `NodeUnpublishVolume` to
unmount the pod bind mount. When no pod on that node is using the volume
anymore, Kubernetes can call `NodeUnstageVolume`, which unmounts the node staging
path and removes the local block device. `ControllerUnpublishVolume` detaches
the Morpheus volume from that node, but does not delete the remote volume. The
PVC/PV remain available for a later pod, including a pod scheduled on another
worker, where CSI will publish and stage the same volume again.

To force a test pod onto a specific labeled worker:

```sh
kubectl delete pod morpheus-csi-testpod --ignore-not-found

kubectl run morpheus-csi-testpod \
  --image=busybox:1.37 \
  --overrides='{"spec":{"nodeName":"k8s-test-1-worker-1","containers":[{"name":"testpod","image":"busybox:1.37","command":["sh","-c","set -eu; echo \"morpheus-csi test write $(date)\" > /data/hello.txt; cat /data/hello.txt; sleep 3600"],"volumeMounts":[{"name":"data","mountPath":"/data"}]}],"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"morpheus-csi-test"}}]}}'
```

## Manual Recovery PV

Kubernetes PVCs do not directly carry a CSI volume UUID. For manual recovery,
create a static `PersistentVolume` with `spec.csi.volumeHandle` set to the
Morpheus server/volume pair, then bind a PVC to that PV with `spec.volumeName`.

Edit and apply:

```sh
kubectl apply -f deploy/kubernetes/examples/recovery-pv.yaml
kubectl apply -f deploy/kubernetes/examples/recovery-pvc.yaml
```

Use this volume handle format:

```text
<morpheus-server-id>:<morpheus-volume-id-or-uuid>
```

The recovery PV uses `persistentVolumeReclaimPolicy: Retain`, so deleting the
PVC/PV will not delete the Morpheus disk.

## Troubleshooting

Controller logs:

```sh
kubectl -n morpheus-csi logs -l app.kubernetes.io/name=morpheus-csi,app.kubernetes.io/component=controller -c morpheus-csi-driver --prefix -f
```

Node plugin logs:

```sh
kubectl -n morpheus-csi get pods -l app.kubernetes.io/name=morpheus-csi,app.kubernetes.io/component=node -o wide
kubectl -n morpheus-csi logs pod/<node-plugin-pod> -c morpheus-csi-driver -f
```

Enable Morpheus API debug logs:

```sh
kubectl -n morpheus-csi patch secret morpheus-csi-credentials --type='json' \
  -p='[{"op":"add","path":"/data/MORPHEUS_DEBUG","value":"dHJ1ZQ=="}]'

kubectl -n morpheus-csi rollout restart deployment/morpheus-csi-controller
```

If a disk appears in Morpheus but not in `lsblk`, the node plugin now attempts
repeated SCSI host rescans automatically. To verify manually on the worker node:

```sh
for host in /sys/class/scsi_host/host*; do
  echo "- - -" > "$host/scan"
done

lsblk -o NAME,PATH,SIZE,TYPE,FSTYPE,MOUNTPOINT,SERIAL,MODEL
```

## Development

```sh
go test ./...
go build ./cmd/morpheus-csi
```
