# Morpheus CSI example overlay

This overlay is the intended edit point for a first deployment.

1. Copy `secret.env.example` to `secret.env`.
2. Edit `secret.env` with the Morpheus URL and API token.
3. Label each Kubernetes worker with its Morpheus server ID, for example `kubectl label node k8s-test-1-worker-2 morpheus.csi/server-id=896`.
4. Edit `storageclass.yaml` with the initial Morpheus server ID, datastore ID, and storage type used when creating new volumes by resize. The example defaults `morpheus.storageType` to `38` for thin.
5. Optionally edit image tags in `kustomization.yaml`.
6. Deploy with `kubectl apply -k deploy/kubernetes/overlays/example`.

For lab Morpheus appliances using a self-signed certificate, set `MORPHEUS_INSECURE_SKIP_VERIFY=true` in `secret.env`. For production, keep it `false` and configure the container trust store or `MORPHEUS_CA_FILE`.

If you change the namespace, update `namespace` in `kustomization.yaml`, `metadata.name` in `namespace.yaml`, and the `csi.storage.k8s.io/*-secret-namespace` values in `storageclass.yaml`.
