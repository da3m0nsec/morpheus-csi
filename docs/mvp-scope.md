# Morpheus CSI MVP Scope

## Current Goal

Build a first usable Container Storage Interface (CSI) driver for Kubernetes that lets a cluster request persistent storage through Morpheus, with the smallest scope that can be tested end to end.

The MVP should prove the complete lifecycle:

1. A `PersistentVolumeClaim` references a Morpheus-backed `StorageClass`.
2. Kubernetes asks the CSI controller to create a volume.
3. The driver creates or maps the corresponding storage object in Morpheus.
4. Kubernetes schedules a pod using the volume.
5. The CSI node service makes the volume available at the kubelet target path.
6. When the claim is deleted and reclaim policy allows it, the driver deletes the Morpheus volume.

## In Scope

### CSI services

- `Identity` service:
  - `GetPluginInfo`
  - `GetPluginCapabilities`
  - `Probe`
- `Controller` service:
  - `CreateVolume`
  - `DeleteVolume`
  - `ControllerGetCapabilities`
  - `ControllerPublishVolume` and `ControllerUnpublishVolume` if the selected Morpheus-backed storage path needs explicit attach/detach.
- `Node` service:
  - `NodeGetInfo`
  - `NodeGetCapabilities`
  - `NodePublishVolume`
  - `NodeUnpublishVolume`
  - `NodeStageVolume` and `NodeUnstageVolume` if the storage path benefits from a per-node staging mount.

### Kubernetes deployment shape

- Controller deployment or statefulset containing:
  - Morpheus CSI driver container.
  - `external-provisioner`.
  - `external-attacher` only if attach/detach is required.
  - `livenessprobe`.
- Node `DaemonSet` containing:
  - Morpheus CSI driver container.
  - `node-driver-registrar`.
  - `livenessprobe`.
- Kubernetes manifests for:
  - `CSIDriver`.
  - RBAC for sidecars.
  - `ServiceAccount`.
  - example `StorageClass`.
  - example `PersistentVolumeClaim`.

### Configuration

- Morpheus API endpoint and token provided through a Kubernetes `Secret`.
- `StorageClass` parameters used for Morpheus placement and storage options.
- Driver name, socket path, kubelet root path, and log level configurable.

Initial secret shape:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: morpheus-csi-credentials
  namespace: morpheus-csi
type: Opaque
stringData:
  MORPHEUS_URL: https://morpheus.example.com
  MORPHEUS_TOKEN: replace-me
```

Initial `StorageClass` parameter ideas, pending API confirmation:

```yaml
parameters:
  morpheus.serverId: "12"
  morpheus.storageType: "38"
  morpheus.datastoreId: "5"
  csi.storage.k8s.io/fstype: ext4
  csi.storage.k8s.io/provisioner-secret-name: morpheus-csi-credentials
  csi.storage.k8s.io/provisioner-secret-namespace: morpheus-csi
  csi.storage.k8s.io/controller-publish-secret-name: morpheus-csi-credentials
  csi.storage.k8s.io/controller-publish-secret-namespace: morpheus-csi
  csi.storage.k8s.io/node-stage-secret-name: morpheus-csi-credentials
  csi.storage.k8s.io/node-stage-secret-namespace: morpheus-csi
```

## Explicitly Out of Scope for MVP

- Snapshots and restore.
- Volume cloning.
- Raw block volumes.
- Windows node support.
- Advanced topology-aware provisioning.
- Volume health monitoring.
- Ephemeral inline CSI volumes.
- Cross-namespace data sources.
- Changed block tracking.
- Multi-backend automatic selection unless it is required by the first Morpheus storage path.

## Required Engineering Properties

- CSI operations must be idempotent.
- `CreateVolume` must tolerate repeated calls with the same name and compatible capacity.
- `DeleteVolume` must tolerate a missing remote volume as success.
- Publish/unpublish and stage/unstage operations must tolerate already-attached, already-mounted, and already-unmounted states.
- Morpheus resource identifiers must be persisted through CSI `VolumeId` and/or volume context.
- Logs must include CSI request IDs or enough operation context to debug Kubernetes events against Morpheus API activity.
- Secrets must never be logged.

## Open Questions Before Implementation

- Which Morpheus server, storage type, and datastore will be supported first?
- Which transport reaches the node: iSCSI, NFS, vSphere disk, cloud disk, or another Morpheus abstraction?
- Does `PUT /api/servers/{id}/resize` return enough volume/device metadata for the node to mount directly?
- How do Kubernetes node names map to Morpheus server IDs after the first manual-server test?
- What minimum Kubernetes version do we target?
- What Morpheus version do we target first?

## Proposed First Milestone

Implement a skeleton Go CSI driver with complete Identity service, capability reporting, config loading, structured logging, and fake/in-memory Morpheus client interfaces. This lets us validate sidecar wiring and CSI behavior before binding the code to a specific storage transport.
