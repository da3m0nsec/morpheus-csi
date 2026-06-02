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
docker build -t ghcr.io/da3m0nsec/morpheus-csi:main .
```

## Deploy

```sh
cp deploy/kubernetes/overlays/example/secret.env.example deploy/kubernetes/overlays/example/secret.env
# Edit deploy/kubernetes/overlays/example/secret.env
# Edit deploy/kubernetes/overlays/example/storageclass.yaml
kubectl apply -k deploy/kubernetes/overlays/example
kubectl -n morpheus-csi get pods
```

The example overlay centralizes the values that normally change between
deployments:

- Morpheus URL and token: `deploy/kubernetes/overlays/example/secret.env`
- Morpheus storage parameters: `deploy/kubernetes/overlays/example/storageclass.yaml`
- Driver and sidecar image tags: `deploy/kubernetes/overlays/example/kustomization.yaml`

If Morpheus uses a self-signed certificate in a lab, set
`MORPHEUS_INSECURE_SKIP_VERIFY=true` in `secret.env`. For production, keep
certificate verification enabled and provide a trusted CA bundle.

To create a test claim after the driver is running:

```sh
kubectl apply -f deploy/kubernetes/examples/pvc.yaml
kubectl apply -f deploy/kubernetes/examples/testpod.yaml
kubectl logs -n default morpheus-csi-testpod
```
