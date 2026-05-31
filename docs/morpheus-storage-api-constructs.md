# Morpheus Storage API Constructs

Sources:

- <https://apidocs.morpheusdata.com/reference/addstoragevolumes>
- <https://apidocs.morpheusdata.com/reference/liststoragevolumes>
- <https://apidocs.morpheusdata.com/reference/getstoragevolumes>
- <https://apidocs.morpheusdata.com/reference/removestoragevolumes>
- <https://apidocs.morpheusdata.com/reference/attachservervolume>
- <https://apidocs.morpheusdata.com/reference/detachservervolume>
- <https://apidocs.morpheusdata.com/reference/liststoragevolumetypes>
- <https://apidocs.morpheusdata.com/reference/liststorageservertypes>
- <https://apidocs.morpheusdata.com/reference/liststorageservers>
- <https://apidocs.morpheusdata.com/reference/getstorageservers>
- <https://apidocs.morpheusdata.com/reference/listclustervolumes>
- <https://apidocs.morpheusdata.com/reference/deleteclustervolume>

## Primary Candidate: Instance Resize

The first implementation uses instance resize as the primary storage operation:

```http
GET /api/instances/{id}?details=true
PUT /api/instances/{id}/resize
```

The Kubernetes `StorageClass` carries the manually selected Morpheus instance ID for the first single-node test. The driver reads the current instance volumes, appends or grows a non-root data volume named after the PVC, and sends the full volume list back through resize.

Likely CSI `StorageClass` parameters:

```yaml
parameters:
  morpheus.instanceId: "12"
  morpheus.storageTypeId: "4"
  morpheus.datastoreId: "5"
  morpheus.deleteOriginalVolumes: "false"
```

CSI `VolumeId` is encoded as `<instanceID>:<volumeID>` so later delete, publish, and expand calls can operate only on the instance that originally received the volume.

## Earlier Candidate: Storage Volumes

The Morpheus API family that most closely maps to CSI dynamic provisioning is `StorageApi`, especially `storage-volumes`.

The API docs describe storage volumes as first-class resources:

```http
GET    /api/storage-volumes
POST   /api/storage-volumes
GET    /api/storage-volumes/{id}
PUT    /api/storage-volumes/{id}
DELETE /api/storage-volumes/{id}
```

Important note from the Morpheus docs: only some storage server types support creating and deleting storage volumes. The docs specifically call out examples such as 3Par and Isilon. Configuration also varies by `Storage Volume Type`; plugin-based storage volume types do not require a `storageGroup`, while built-in storage volume types do.

For the CSI driver, this means `StorageClass` must carry enough placement/type information to let the driver construct a valid `POST /api/storage-volumes` request for the selected backend.

## Discovery Constructs

Before implementing `CreateVolume`, we need to inspect the target Morpheus appliance and confirm which storage backends are available.

### Storage Servers

```http
GET /api/storage-servers
GET /api/storage-servers/{id}
```

Use this to identify the Morpheus storage server that will own provisioned volumes.

Likely CSI `StorageClass` parameter:

```yaml
parameters:
  morpheus.storageServerId: "12"
```

### Storage Server Types

```http
GET /api/storage-server-types
GET /api/storage-server-types/{id}
```

The list endpoint supports pagination, sorting, and filters such as `name`, `code`, and `phrase`.

Use this to understand backend capabilities and option metadata. This is useful during setup and validation, but the CSI hot path should normally receive a concrete `storageServerId`.

### Storage Volume Types

```http
GET /api/storage-volume-types
GET /api/storage-volume-types/{id}
```

The list endpoint supports pagination and filters such as `name`, `code`, and `phrase`.

Use this to discover which volume type code or ID should be used in `CreateVolume`.

Likely CSI `StorageClass` parameters:

```yaml
parameters:
  morpheus.storageVolumeTypeId: "4"
  morpheus.storageVolumeTypeCode: "isilon-volume"
```

Only one of ID or code should be required in the final design. Prefer ID if Morpheus requires it in create requests; prefer code only if the API accepts stable codes.

## CSI Operation Mapping

### `CreateVolume`

Candidate Morpheus call:

```http
POST /api/storage-volumes
```

Inputs from CSI:

- `CreateVolumeRequest.name`: Morpheus volume name, possibly prefixed.
- `CreateVolumeRequest.capacity_range`: requested size.
- `CreateVolumeRequest.parameters`: `StorageClass` parameters.
- `CreateVolumeRequest.secrets`: Morpheus API credentials.

Inputs we expect from `StorageClass`:

- `morpheus.storageServerId`
- `morpheus.storageVolumeTypeId` or `morpheus.storageVolumeTypeCode`
- `morpheus.storageGroup`, if required by the selected built-in type.
- optional backend-specific configuration keys under a namespaced prefix such as `morpheus.config.*`.

CSI idempotency requirement:

- If Morpheus already has a volume for the CSI volume name, return the existing volume when size and placement are compatible.
- If an existing volume has incompatible size/type/placement, fail with an appropriate CSI error.

### `DeleteVolume`

Candidate Morpheus call:

```http
DELETE /api/storage-volumes/{id}
```

CSI idempotency requirement:

- Missing Morpheus volume should be treated as success.
- If Morpheus deletion is asynchronous, the driver may need to poll `GET /api/storage-volumes/{id}` or return success once Morpheus has accepted the delete request, depending on observed API behavior.

### `ControllerPublishVolume`

Candidate Morpheus call:

```http
PUT /api/servers/{id}/volumes/{volumeId}/attach
```

The docs describe this as attaching an existing storage volume to a server, optionally specifying mount point controller and unit number. It is available for HVM.

CSI inputs:

- CSI `VolumeId`: Morpheus storage volume ID.
- CSI `NodeId`: must map to a Morpheus server ID.

Open mapping question:

- Kubernetes node name is not automatically a Morpheus server ID. We need a deterministic node mapping strategy before enabling this capability.

Likely options:

- Node annotation containing Morpheus server ID.
- Driver startup config mapping Kubernetes node names to Morpheus server IDs.
- Morpheus lookup by hostname, UUID, or cloud instance metadata.

### `ControllerUnpublishVolume`

Candidate Morpheus call:

```http
PUT /api/servers/{id}/volumes/{volumeId}/detach
```

CSI idempotency requirement:

- Detaching an already detached volume should be treated as success if the Morpheus API exposes enough state to confirm it.

### `NodeStageVolume` / `NodePublishVolume`

The Morpheus storage API can create and attach a volume, but it does not by itself tell us the node-level Linux operation.

We still need the selected backend to answer:

- After attach, what device path appears on the node?
- Is the transport block device, iSCSI, NFS, or another protocol?
- Who formats the filesystem?
- Does Morpheus return connection data, or does the node discover the device through the OS?

Until this is known, node-side implementation remains backend-specific.

## Cluster Volume Endpoints

The API also exposes cluster-scoped volume resources:

```http
GET    /api/clusters/{clusterId}/volumes
GET    /api/clusters/{clusterId}/volumes/{id}
DELETE /api/clusters/{clusterId}/volumes/{id}
GET    /api/clusters/{clusterId}/volumeclaims
GET    /api/clusters/{clusterId}/volumeclaims/{id}
```

These look like Morpheus views of Kubernetes cluster resources rather than the general storage provisioning API. They may be useful for reconciliation or diagnostics when Morpheus manages the cluster, but they should not be assumed to satisfy CSI `CreateVolume` until we confirm there is a create/update API and that it represents external persistent storage.

## Implemented Morpheus Client Interfaces

```go
type InstanceVolumeClient interface {
    EnsureVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error)
    DeleteVolume(ctx context.Context, ref VolumeRef) error
    ExpandVolume(ctx context.Context, req ResizeVolumeRequest) (*StorageVolume, error)
    GetVolume(ctx context.Context, ref VolumeRef) (*StorageVolume, error)
}

type StorageDiscoveryClient interface {
    ValidateStorageClass(ctx context.Context, parameters map[string]string) error
}
```

The first implementation keeps the Morpheus instance ID in `StorageClass` and encodes CSI volume IDs as `<instanceID>:<volumeID>`.

## StorageClass Draft

```yaml
apiVersion: storage.k8s.io/v1
kind: StorageClass
metadata:
  name: morpheus-storage
provisioner: csi.morpheusdata.com
reclaimPolicy: Delete
volumeBindingMode: WaitForFirstConsumer
allowVolumeExpansion: true
parameters:
  morpheus.instanceId: "12"
  morpheus.storageTypeId: "4"
  morpheus.datastoreId: "5"
  morpheus.deleteOriginalVolumes: "false"
  csi.storage.k8s.io/fstype: ext4
  csi.storage.k8s.io/provisioner-secret-name: morpheus-csi-credentials
  csi.storage.k8s.io/provisioner-secret-namespace: morpheus-csi
  csi.storage.k8s.io/controller-publish-secret-name: morpheus-csi-credentials
  csi.storage.k8s.io/controller-publish-secret-namespace: morpheus-csi
```

## Open Items

- Capture actual request and response payloads for:
  - `GET /api/instances/{id}?details=true`
  - `PUT /api/instances/{id}/resize`
- Confirm whether volume size is expressed in bytes, MiB, GiB, or backend-specific units.
- Confirm which field stores a stable external ID/name usable for CSI idempotency.
- Confirm which instance volume field, if any, exposes the node device path after resize.
- Confirm the future node mapping from Kubernetes nodes to Morpheus instance IDs.
