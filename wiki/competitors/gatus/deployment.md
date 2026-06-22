# Gatus — Deployment, Performance & Security

## Installation & Deployment

### Docker (Recommended)

**Quick Start**:
```bash
docker run -d \
  --name gatus \
  -p 8080:8080 \
  -v $(pwd)/config:/config \
  -v $(pwd)/data:/data \
  twinproduction/gatus:latest
```

**Docker Compose**:
```yaml
version: '3.8'
services:
  gatus:
    image: twinproduction/gatus:latest
    container_name: gatus
    ports:
      - "8080:8080"
    volumes:
      - ./config:/config
      - ./data:/data
    restart: unless-stopped
```

### Binary Installation

**Download**:
```bash
# Download latest release
wget https://github.com/TwiN/gatus/releases/download/v5.x.x/gatus-linux-amd64

# Make executable
chmod +x gatus-linux-amd64

# Run
./gatus-linux-amd64 --config=config/config.yaml
```

### From Source

**Build from source**:
```bash
git clone https://github.com/TwiN/gatus.git
cd gatus
go build -o gatus .
./gatus
```

### Kubernetes

**Helm chart available**:
```bash
helm repo add gatus https://gatus.io/charts
helm install gatus gatus/gatus
```

## Performance & Scalability

### Resource Requirements

**Minimum**:
- **CPU**: 0.5 core
- **RAM**: 128 MB
- **Disk**: 100 MB (stateless) or 1 GB (with storage)

**Recommended**:
- **CPU**: 1 core
- **RAM**: 256-512 MB
- **Disk**: 5 GB (with PostgreSQL)

### Scalability

**Horizontal scaling**:
- Limited - designed as single instance
- Can run multiple instances with separate configs
- No built-in distributed monitoring

**Monitoring capacity**:
- Can handle 1000+ endpoints comfortably
- Efficient Go concurrency
- Low memory per endpoint
- Performance depends on check intervals

**Database choice impact**:
- **In-memory**: Fastest, no persistence
- **SQLite**: Good for 100-500 endpoints
- **PostgreSQL**: Scalable to 1000+ endpoints

## Security

### Authentication

**Basic auth**:
```yaml
security:
  basic:
    username: "admin"
    password-sha512: "hashed_password"
```

**OIDC** (OpenID Connect):
```yaml
security:
  oidc:
    issuer-url: "https://auth.example.com"
    redirect-url: "https://status.example.com/authorization-code/callback"
    client-id: "${OIDC_CLIENT_ID}"
    client-secret: "${OIDC_CLIENT_SECRET}"
```

### TLS/HTTPS

**Built-in TLS**:
```yaml
web:
  address: 0.0.0.0
  port: 8443
  tls:
    certificate-file: "cert.pem"
    private-key-file: "key.pem"
```

**Or use reverse proxy** (recommended):
- Nginx, Traefik, Caddy for HTTPS
- Better certificate management

### Secrets Management

**Environment variables**:
```yaml
alerting:
  slack:
    webhook-url: "${SLACK_WEBHOOK_URL}"

storage:
  postgres:
    url: "${DATABASE_URL}"
```

**Best practices**:
- Use environment variables for secrets
- Never commit secrets to Git
- Use secret management tools (Vault, etc.)
