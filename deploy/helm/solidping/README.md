# SolidPing Helm chart

```bash
helm install solidping oci://ghcr.io/fclairamb/charts/solidping \
  --set baseUrl=https://status.example.com \
  --set ingress.enabled=true --set 'ingress.hosts[0].host=status.example.com' \
  --set 'ingress.hosts[0].paths[0].path=/' --set 'ingress.hosts[0].paths[0].pathType=Prefix'
```

The chart is published with each release, at the release version. From a
checkout, use `./deploy/helm/solidping` instead of the OCI reference.

SQLite on a 1 Gi volume by default (single replica, `Recreate` strategy). For
PostgreSQL create a Secret with the connection string under `url` and set
`database.type=postgres`, `database.existingSecret=<name>`.

The JWT signing key is generated on first install and kept across upgrades.
Set `auth.existingSecret` (key `jwt-secret`) to manage it yourself.

First login is `admin@solidping.io` / `solidpass`. Change it immediately.
