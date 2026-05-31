# morpheus-csi

CSI driver MVP for provisioning Morpheus-backed storage in Kubernetes.

## Current state

- Kubernetes deployment manifests live in `deploy/kubernetes/`.
- The Go driver exposes CSI Identity, Controller, and Node services.
- Controller RPCs call the Morpheus storage volume and server attach/detach APIs.
- Node RPCs support staged filesystem mounts and pod bind mounts for block devices.
- Before using against a real cluster, validate the exact Morpheus create/attach payloads and confirm that attach responses include `morpheus.devicePath` or another device path mapping.

## Build

```sh
go test ./...
go build ./cmd/morpheus-csi
docker build -t ghcr.io/da3m0nsec/morpheus-csi:dev .
```
