# Kubernetes CSI Developing Summary

Source: <https://kubernetes-csi.github.io/docs/developing.html>

## Core Model

A Kubernetes CSI driver is an application that implements the CSI gRPC services. The Kubernetes CSI developer documentation says the minimum driver surface is:

- CSI `Identity` service, so Kubernetes components and CSI sidecars can identify the driver and discover optional functionality.
- CSI `Node` service, specifically `NodePublishVolume`, `NodeUnpublishVolume`, and `NodeGetCapabilities`.

For the Morpheus CSI MVP, we need more than the minimum because we want dynamic provisioning. That means implementing the optional `Controller` service and advertising the right controller capabilities.

## Services We Care About

### Identity

Required for all drivers.

- `GetPluginInfo`: returns the stable driver name, for example `csi.morpheusdata.com`.
- `GetPluginCapabilities`: advertises plugin-level features, including controller service support if present.
- `Probe`: reports whether the plugin is healthy enough to serve requests.

### Controller

Required for dynamic provisioning and attach/detach integrations.

- `CreateVolume`: create or find the Morpheus-backed volume for a PVC.
- `DeleteVolume`: delete the Morpheus-backed volume.
- `ControllerPublishVolume`: attach or authorize a volume for a node when the backend needs it.
- `ControllerUnpublishVolume`: reverse the publish/attach step.
- `ControllerGetCapabilities`: tells sidecars which controller RPCs are supported.

Relevant capabilities:

- `CREATE_DELETE_VOLUME`: needed when using dynamic provisioning.
- `PUBLISH_UNPUBLISH_VOLUME`: needed if Kubernetes attach/detach should call the driver.

### Node

Required for making volumes visible to pods on each node.

- `NodeGetInfo`: returns the node ID used by the driver.
- `NodeGetCapabilities`: advertises node capabilities.
- `NodeStageVolume` / `NodeUnstageVolume`: optional per-node staging mount/device setup.
- `NodePublishVolume` / `NodeUnpublishVolume`: bind-mount or otherwise expose the volume at the pod target path.

Relevant capability:

- `STAGE_UNSTAGE_VOLUME`: advertise only if we implement staging.

## Sidecars

Kubernetes CSI drivers normally run with standard sidecar containers rather than directly watching all Kubernetes objects themselves.

- `external-provisioner`: watches PVCs and calls `CreateVolume`/`DeleteVolume`. It passes `StorageClass` parameters to `CreateVolumeRequest.parameters`. It also handles reserved `csi.storage.k8s.io/*` secret and filesystem parameters.
- `external-attacher`: watches `VolumeAttachment` objects and calls `ControllerPublishVolume`/`ControllerUnpublishVolume`. Use it only if the Morpheus storage path requires attach/detach.
- `node-driver-registrar`: runs on every node, discovers driver info through `NodeGetInfo`, and registers the driver socket with kubelet.
- `livenessprobe`: checks whether the CSI endpoint is responsive.
- `external-resizer` is required for PVC expansion. `external-snapshotter` and health-monitor sidecars are out of scope for the MVP.

## Kubernetes Objects

The MVP should produce manifests for these objects:

- `CSIDriver`: declares the driver to Kubernetes and controls behavior such as attach requirements.
- `StorageClass`: points to the CSI driver by its provisioner name and carries Morpheus placement/options.
- `PersistentVolumeClaim`: the user-facing claim that triggers provisioning.
- `PersistentVolume`: created by the external provisioner after `CreateVolume`.
- `VolumeAttachment`: created by Kubernetes when attach/detach is in use.
- `CSINode`: maintained by Kubernetes to track CSI drivers available on nodes.

## Deployment Pattern

The documentation separates controller and node components:

- Controller component:
  - Runs controller-side sidecars and the CSI driver.
  - Communicates with sidecars over a shared UNIX domain socket, usually through an `emptyDir`.
  - Needs RBAC for the Kubernetes objects each sidecar watches.
- Node plugin:
  - Runs as a `DaemonSet` on every node.
  - Includes the CSI driver and `node-driver-registrar`.
  - Shares a UNIX domain socket with kubelet through host paths.
  - Needs host access and mount propagation so mounts created in the driver container are visible to kubelet and pods.

## StorageClass and Secrets

`StorageClass.parameters` are passed as opaque CSI parameters except reserved keys prefixed with `csi.storage.k8s.io/`.

Reserved keys we expect to use:

- `csi.storage.k8s.io/fstype`
- `csi.storage.k8s.io/provisioner-secret-name`
- `csi.storage.k8s.io/provisioner-secret-namespace`
- `csi.storage.k8s.io/controller-publish-secret-name`
- `csi.storage.k8s.io/controller-publish-secret-namespace`
- `csi.storage.k8s.io/node-stage-secret-name`
- `csi.storage.k8s.io/node-stage-secret-namespace`
- `csi.storage.k8s.io/node-publish-secret-name`
- `csi.storage.k8s.io/node-publish-secret-namespace`

The Morpheus-specific keys should avoid the reserved prefix, for example `morpheus.zoneId`, `morpheus.siteId`, or `morpheus.storageType`.

## MVP Implications

- Use standard sidecars rather than writing Kubernetes controllers.
- Implement `CreateVolume` and `DeleteVolume` from the start.
- Include `external-attacher` only if the first Morpheus backend needs attach/detach.
- Keep snapshots, topology, raw block, and health monitoring out of the first release.
- Make driver capabilities match actual implementation; over-advertising capabilities will cause Kubernetes to call unsupported paths.
