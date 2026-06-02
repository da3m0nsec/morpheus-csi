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
  current MVP assumes a manually selected Morpheus server ID.
- The node plugin runs privileged and performs a SCSI host rescan before staging
  a volume, because newly attached VM disks may not appear in `lsblk` until the
  bus is rescanned.

## Known Limitations

- Node/device mapping is not fully automatic yet. The node stage path still needs
  a usable `morpheus.devicePath` or an equivalent discovery mechanism.
- Scheduling must currently be aligned with the Morpheus server configured in
  the `StorageClass`. If the PVC is provisioned on one VM but the pod lands on a
  different Kubernetes node, the disk will not appear where kubelet needs it.
- Dynamic per-node Morpheus server lookup is not implemented yet.
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
- `morpheus.serverId`
- `morpheus.storageType`, default example `38`
- `morpheus.datastoreId`

## Test PVC

Create the test claim and pod:

```sh
kubectl apply -f deploy/kubernetes/examples/pvc.yaml
kubectl apply -f deploy/kubernetes/examples/testpod.yaml
```

For the current single-server workflow, schedule the test pod onto the Kubernetes
node that matches `morpheus.serverId`:

```sh
kubectl delete pod morpheus-csi-testpod --ignore-not-found

kubectl run morpheus-csi-testpod \
  --image=busybox:1.37 \
  --overrides='{"spec":{"nodeName":"k8s-test-1-worker-1","containers":[{"name":"testpod","image":"busybox:1.37","command":["sh","-c","set -eu; echo \"morpheus-csi test write $(date)\" > /data/hello.txt; cat /data/hello.txt; sleep 3600"],"volumeMounts":[{"name":"data","mountPath":"/data"}]}],"volumes":[{"name":"data","persistentVolumeClaim":{"claimName":"morpheus-csi-test"}}]}}'
```

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

If a disk appears in Morpheus but not in `lsblk`, the node plugin now attempts a
SCSI rescan automatically. To verify manually on the worker node:

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

