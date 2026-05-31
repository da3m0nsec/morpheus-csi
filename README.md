# morpheus-csi

CSI driver MVP for provisioning Morpheus-backed storage in Kubernetes.

## Current state

- Kubernetes deployment manifests live in `deploy/kubernetes/`.
- The Go driver exposes CSI Identity, Controller, and Node services.
- Controller RPCs use Morpheus instance resize to add, remove, and grow data volumes on a manually selected Kubernetes node instance.
- Node RPCs support staged filesystem mounts and pod bind mounts for block devices.
- Before using against a real cluster, validate the exact Morpheus instance resize payload and confirm that instance volume details include `morpheus.devicePath` or another device path mapping.

## Build

```sh
go test ./...
go build ./cmd/morpheus-csi
docker build -t ghcr.io/da3m0nsec/morpheus-csi:dev .
```

## Deploy

```sh
kubectl apply -f deploy/kubernetes/
kubectl -n morpheus-csi get pods
```
