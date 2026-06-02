# Morpheus CSI example overlay

This overlay is the intended edit point for a first deployment.

1. Copy `secret.env.example` to `secret.env`.
2. Edit `secret.env` with the Morpheus URL and API token.
3. Edit `storageclass.yaml` with the Morpheus server, storage type, and datastore IDs.
4. Optionally edit image tags in `kustomization.yaml`.
5. Deploy with `kubectl apply -k deploy/kubernetes/overlays/example`.

For lab Morpheus appliances using a self-signed certificate, set `MORPHEUS_INSECURE_SKIP_VERIFY=true` in `secret.env`. For production, keep it `false` and configure the container trust store or `MORPHEUS_CA_FILE`.

If you change the namespace, update `namespace` in `kustomization.yaml`, `metadata.name` in `namespace.yaml`, and the `csi.storage.k8s.io/*-secret-namespace` values in `storageclass.yaml`.
