---
sidebar_position: 3
title: Kubernetes
---

# Kubernetes Deployment

SolidPing can be deployed on Kubernetes for high availability and scalability.

## Prerequisites

- Kubernetes cluster (1.20+)
- kubectl configured
- PostgreSQL database (managed or self-hosted)

## Basic Deployment

### Namespace and Secret

```yaml
# namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: solidping
---
# secret.yaml
apiVersion: v1
kind: Secret
metadata:
  name: solidping-secrets
  namespace: solidping
type: Opaque
stringData:
  db-url: "postgresql://user:password@postgres-host:5432/solidping?sslmode=require"
```

### Deployment

```yaml
# deployment.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: solidping
  namespace: solidping
  labels:
    app: solidping
spec:
  replicas: 2
  selector:
    matchLabels:
      app: solidping
  template:
    metadata:
      labels:
        app: solidping
    spec:
      containers:
        - name: solidping
          image: ghcr.io/fclairamb/solidping:latest
          ports:
            - containerPort: 4000
          env:
            - name: SP_DB_TYPE
              value: "postgres"
            - name: SP_DB_URL
              valueFrom:
                secretKeyRef:
                  name: solidping-secrets
                  key: db-url
            - name: SP_SERVER_LISTEN
              value: ":4000"
          resources:
            requests:
              memory: "128Mi"
              cpu: "100m"
            limits:
              memory: "512Mi"
              cpu: "500m"
          livenessProbe:
            httpGet:
              path: /api/mgmt/health
              port: 4000
            initialDelaySeconds: 10
            periodSeconds: 30
          readinessProbe:
            httpGet:
              path: /api/mgmt/health
              port: 4000
            initialDelaySeconds: 5
            periodSeconds: 10
```

### Service

```yaml
# service.yaml
apiVersion: v1
kind: Service
metadata:
  name: solidping
  namespace: solidping
spec:
  selector:
    app: solidping
  ports:
    - protocol: TCP
      port: 80
      targetPort: 4000
  type: ClusterIP
```

### Ingress

```yaml
# ingress.yaml
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: solidping
  namespace: solidping
  annotations:
    kubernetes.io/ingress.class: nginx
    cert-manager.io/cluster-issuer: letsencrypt-prod
spec:
  tls:
    - hosts:
        - monitoring.example.com
      secretName: solidping-tls
  rules:
    - host: monitoring.example.com
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: solidping
                port:
                  number: 80
```

## Apply Configuration

```bash
kubectl apply -f namespace.yaml
kubectl apply -f secret.yaml
kubectl apply -f deployment.yaml
kubectl apply -f service.yaml
kubectl apply -f ingress.yaml
```

## File Storage

SolidPing writes a handful of blobs outside the database — org logos,
status-page assets, incident screenshots. By default
(`SP_FILESTORAGE_TYPE=local`) they land at `SP_FILESTORAGE_LOCAL_ROOT`
(default `./data/files`), which is relative to the container's working
directory. **Unless that path is backed by a mounted volume, every upload is
destroyed the next time the pod is recreated** — a rollout, a reschedule,
anything — and it fails silently: nothing errors at write time, only a later
read. See [File Storage](/configuration/file-storage) for the full guide.

### Option A — PersistentVolumeClaim (single replica)

Add a PVC and mount it at an absolute path, so `SP_FILESTORAGE_LOCAL_ROOT` has
no working-directory ambiguity:

```yaml
# solidping-files-pvc.yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: solidping-files
  namespace: solidping
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: 5Gi
```

```yaml
    spec:
      containers:
        - name: solidping
          image: ghcr.io/fclairamb/solidping:latest
          env:
            - name: SP_FILESTORAGE_LOCAL_ROOT
              value: "/data/files"
          volumeMounts:
            - name: files
              mountPath: /data/files
      volumes:
        - name: files
          persistentVolumeClaim:
            claimName: solidping-files
```

:::caution ReadWriteOnce caps you at one replica
A `ReadWriteOnce` PVC can only be mounted by pods scheduled on a single node,
so a `Deployment` using it cannot scale past `replicas: 1` — the sample
`Deployment` above runs `replicas: 2`, which this option is incompatible with.
Drop to a single replica, or use the S3 backend below if you need more than
one.
:::

### Option B — S3 backend (multi-replica)

The S3 backend keeps no local state, so it works at any replica count:

```yaml
          env:
            - name: SP_FILESTORAGE_TYPE
              value: "s3"
            - name: SP_FILESTORAGE_S3_BUCKET
              value: "solidping"
            - name: SP_FILESTORAGE_S3_REGION
              value: "us-east-1"
            - name: SP_FILESTORAGE_S3_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: solidping-secrets
                  key: s3-access-key
            - name: SP_FILESTORAGE_S3_SECRET_KEY
              valueFrom:
                secretKeyRef:
                  name: solidping-secrets
                  key: s3-secret-key
```

See [File Storage](/configuration/file-storage) for the endpoint/path-style
settings a non-AWS S3-compatible store needs, and the full variable reference.

## With Helm (Coming Soon)

A Helm chart for SolidPing is planned for future releases.

## PostgreSQL on Kubernetes

If you need PostgreSQL on the same cluster:

```yaml
# postgres.yaml
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgres
  namespace: solidping
spec:
  serviceName: postgres
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          ports:
            - containerPort: 5432
          env:
            - name: POSTGRES_USER
              value: solidping
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: solidping-secrets
                  key: postgres-password
            - name: POSTGRES_DB
              value: solidping
          volumeMounts:
            - name: postgres-data
              mountPath: /var/lib/postgresql/data
  volumeClaimTemplates:
    - metadata:
        name: postgres-data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: 10Gi
---
apiVersion: v1
kind: Service
metadata:
  name: postgres
  namespace: solidping
spec:
  selector:
    app: postgres
  ports:
    - port: 5432
  clusterIP: None
```

## Distributed Workers

To deploy workers in different regions, create separate deployments with region-specific configuration:

```yaml
# worker-us-east.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: solidping-worker-us-east
  namespace: solidping
spec:
  replicas: 1
  selector:
    matchLabels:
      app: solidping-worker
      region: us-east
  template:
    metadata:
      labels:
        app: solidping-worker
        region: us-east
    spec:
      containers:
        - name: solidping
          image: ghcr.io/fclairamb/solidping:latest
          env:
            - name: SP_REGION
              value: "us-east-1"
            # ... other environment variables
```

## Browser checks (headless Chrome sidecar)

The SolidPing image is distroless and ships no browser. To run
[browser checks](/features/check-types#browser), add a headless Chrome
**sidecar to each checks-worker Deployment** and talk to it over localhost:

```yaml
    spec:
      containers:
        - name: solidping
          image: ghcr.io/fclairamb/solidping:latest
          env:
            - name: SP_CHECKERS_BROWSER_CDP_URL
              value: "ws://127.0.0.1:9222"
        - name: browser
          image: chromedp/headless-shell:151.0.7922.109
          args:
            - --remote-debugging-address=0.0.0.0
            - --remote-debugging-port=9222
            # Required from Chrome 111 on for a non-browser websocket client.
            - --remote-allow-origins=*
            - --disable-gpu
            - --no-sandbox
          ports:
            - containerPort: 9222
          volumeMounts:
            # Chrome mounts /dev/shm; the container default is far too small.
            - name: dshm
              mountPath: /dev/shm
          resources:
            requests: { memory: "512Mi" }
            limits: { memory: "2Gi" }
      volumes:
        - name: dshm
          emptyDir:
            medium: Memory
            sizeLimit: 1Gi
```

A sidecar rather than a shared browser Deployment: the CDP endpoint stays on
localhost (never exposed as a Service — a reachable CDP endpoint is remote
control of a browser), and the browser's lifecycle is coupled to the worker
that uses it. Pin the image tag so every region runs the same Chrome version.

Each worker caps itself at 4 concurrent browser executions, so size the sidecar
for four tabs. Workers that can reach their sidecar advertise a `browser`
capability per region; if the sidecar dies, checks report an **error** (not a
"down") and the region stops advertising the capability on the next heartbeat.

## Monitoring

SolidPing exposes metrics that can be scraped by Prometheus:

```yaml
# servicemonitor.yaml
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: solidping
  namespace: solidping
spec:
  selector:
    matchLabels:
      app: solidping
  endpoints:
    - port: http
      path: /metrics
```

## Next Steps

- [Configuration Guide](/configuration) - All configuration options
- [File Storage](/configuration/file-storage) - Local volume vs. S3 for uploaded blobs
- [Check Types](/features/check-types) - Configure your first checks
