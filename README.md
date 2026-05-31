# morpheus-csi

CSI driver MVP for provisioning Morpheus-backed storage in Kubernetes.

## Current state

- Kubernetes deployment manifests live in `deploy/kubernetes/`.
- The Go driver skeleton exposes CSI Identity, Controller, and Node services.
- Volume lifecycle, attach/detach, and node mount operations are present as CSI RPCs but intentionally return `Unimplemented` until the Morpheus storage API integration is added.

## Build

```sh
go test ./...
go build ./cmd/morpheus-csi
docker build -t ghcr.io/morpheusdata/morpheus-csi:dev .
```
