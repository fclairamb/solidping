# Gatus - Complete Analysis

## Overview

Gatus is an advanced, developer-oriented health dashboard and status page system created by TwinProduction (Chris Gervais). It's positioned as "the most advanced status page in the world" with a focus on configuration-as-code, lightweight architecture, and powerful condition-based monitoring.

**GitHub**: https://github.com/TwiN/gatus

**Website**: https://gatus.io

**License**: Apache 2.0

**Technology**: Go (backend), simple HTML/CSS/JS (frontend)

**Database**: SQLite or PostgreSQL (optional)

**Current Version**: v5.34.0 (as of March 2026)

## Key Statistics

- **GitHub Stars**: 10,100+ (Mar 2026)
- **Forks**: 400+
- **Releases**: 100+ versions
- **Contributors**: 50+ community members
- **Active Development**: Very active (2019-present)
- **Written in**: 100% Go
- **Focus**: Developer-centric, YAML-configured

## Key Features

### Core Philosophy

**Configuration as Code**:
- **YAML-based**: All configuration in version-controlled files
- **No database required**: Can run stateless
- **GitOps-friendly**: Commit-based config management
- **Declarative**: Define desired state, not procedures
- **Infrastructure as code**: Terraform-compatible approach

### Monitor Types (Endpoints)

Gatus monitors **endpoints** (not "monitors"):

1. **HTTP/HTTPS** - Web and API monitoring
2. **TCP** - Port connectivity checks
3. **ICMP** - Ping monitoring
4. **DNS** - DNS resolution validation
5. **WebSocket** - WebSocket connection monitoring
6. **SSH** - SSH server connectivity
7. **TLS** - TLS/SSL certificate monitoring
8. **STARTTLS** - STARTTLS protocol support (SMTP, IMAP, etc.)
9. **External** - Call external scripts/commands

**Notable**: 9 endpoint types with extensive protocol coverage

### Condition-Based Monitoring

**Powerful condition syntax**:
- **Declarative conditions**: Define what "healthy" means
- **Multiple placeholders**: Status, body, response time, certificate, IP, etc.
- **JSONPath support**: Query JSON responses
- **Logical operators**: AND, OR, NOT operations
- **Functions**: len(), has(), pat() for advanced checks
- **Flexible thresholds**: Per-endpoint success criteria

**Example Conditions**:
```yaml
conditions:
  - "[STATUS] == 200"                       # Status code
  - "[BODY].status == UP"                   # JSONPath
  - "[RESPONSE_TIME] < 300"                 # Response time (ms)
  - "[CERTIFICATE_EXPIRATION] > 48h"        # Certificate expiry
  - "len([BODY].data) > 0"                  # Array length
  - "has([BODY].errors) == false"           # Field existence
  - "[IP] == 192.168.1.1"                   # IP validation
```

### Alerting System

**20+ alert providers**:
- **Slack**: Native integration
- **Discord**: Discord webhook support
- **Microsoft Teams**: Teams webhook
- **PagerDuty**: Incident management
- **Opsgenie**: Alert management
- **Email**: SMTP support
- **Twilio**: SMS via Twilio
- **GitHub**: Auto-create/close issues
- **GitLab**: GitLab issue integration
- **Gitea**: Gitea issue support
- **Matrix**: Matrix protocol
- **Mattermost**: Mattermost webhooks
- **Pushover**: Pushover notifications
- **Ntfy**: Ntfy.sh push notifications
- **Home Assistant**: HA event triggering
- **AWS SES**: Email via AWS SES
- **Custom webhooks**: HTTP callbacks
- **And more**: Telegram, Zulip, etc.

**Alert Configuration**:
- **Thresholds**: Define failure/success counts
- **Reminders**: Periodic re-notification
- **send-on-resolved**: Alert when service recovers
- **Custom descriptions**: Template alert messages
- **Per-endpoint alerts**: Different providers per endpoint

### Status Pages

- **Public status pages**: Customer-facing dashboards
- **Real-time updates**: Automatic page refresh
- **Logo customization**: Custom branding
- **Historical data**: Uptime history display
- **Incident timeline**: Show past incidents
- **Response time graphs**: Performance visualization
- **Multiple pages**: Not mentioned (single page focus)
- **Dark theme**: Built-in dark mode

### Advanced Features

**Observability**:
- **Prometheus metrics**: `/metrics` endpoint
- **Grafana integration**: Pre-built dashboards
- **Health checks**: Own health endpoint
- **Performance metrics**: Response time tracking

**Storage Options**:
- **In-memory**: No persistence (stateless)
- **SQLite**: File-based persistence
- **PostgreSQL**: Production-grade database
- **Hybrid**: Memory with periodic backups

**Security**:
- **Basic auth**: Dashboard protection
- **OIDC**: OpenID Connect support
- **API keys**: Secure API access
- **TLS**: HTTPS support

## Technology Stack

### Backend
- **Language**: Go (100%)
- **Performance**: Compiled binary, fast execution
- **Concurrency**: Go routines for parallel checks
- **Memory**: Low memory footprint
- **Binary size**: Small (< 20 MB)

### Frontend
- **Stack**: Simple HTML, CSS, JavaScript
- **No framework**: Vanilla JS for performance
- **Real-time**: Auto-refresh, no WebSocket needed
- **Responsive**: Mobile-friendly design

### Storage
- **Options**: In-memory, SQLite, PostgreSQL
- **Default**: SQLite (if persistence enabled)
- **Flexibility**: Choose based on needs
- **Migration**: Easy database switching

## Configuration

### YAML Configuration

**Main config file**: `config/config.yaml` (or custom path)

**Structure**:
```yaml
# Storage configuration
storage:
  type: sqlite
  path: data/gatus.db

# Security
security:
  basic:
    username: admin
    password-sha512: "hashed_password"

# Alerting providers
alerting:
  slack:
    webhook-url: "https://hooks.slack.com/..."
    default-alert:
      description: "Health check failed"
      send-on-resolved: true

# Web UI configuration
web:
  address: 0.0.0.0
  port: 8080

# UI customization
ui:
  title: "Status Page"
  header: "Service Status"
  logo: "https://example.com/logo.png"

# Endpoints to monitor
endpoints:
  - name: website
    group: production
    url: "https://example.com"
    interval: 5m
    conditions:
      - "[STATUS] == 200"
      - "[RESPONSE_TIME] < 500"
    alerts:
      - type: slack
        enabled: true

  - name: api
    url: "https://api.example.com/health"
    interval: 1m
    conditions:
      - "[STATUS] == 200"
      - "[BODY].status == healthy"
      - "len([BODY].errors) == 0"
    alerts:
      - type: pagerduty
        send-on-resolved: true
```

### Endpoint Configuration

**Required fields**:
- `name` - Endpoint name
- `url` - Target URL
- `conditions` - Health check conditions

**Optional fields**:
- `group` - Group organization
- `interval` - Check frequency (default: 1m)
- `client` - HTTP client config
- `alerts` - Alert configuration
- `ui` - UI display options

**HTTP Client Options**:
```yaml
client:
  timeout: 10s
  insecure: false  # Skip TLS verification
  oauth2:
    token-url: "..."
    client-id: "..."
    client-secret: "..."
```

### Condition Placeholders

**Available placeholders**:

| Placeholder | Description | Example |
|-------------|-------------|---------|
| `[STATUS]` | HTTP status code | `[STATUS] == 200` |
| `[BODY]` | Response body (JSONPath) | `[BODY].status == UP` |
| `[RESPONSE_TIME]` | Response time (ms) | `[RESPONSE_TIME] < 1000` |
| `[IP]` | Resolved IP address | `[IP] == 1.2.3.4` |
| `[CERTIFICATE_EXPIRATION]` | Certificate validity | `[CERTIFICATE_EXPIRATION] > 168h` |
| `[DOMAIN_EXPIRATION]` | Domain expiry | `[DOMAIN_EXPIRATION] > 720h` |
| `[DNS_RCODE]` | DNS response code | `[DNS_RCODE] == NOERROR` |
| `[CONNECTED]` | Connection status | `[CONNECTED] == true` |

**Functions**:
- `len([BODY].array)` - Array/string length
- `has([BODY].field)` - Field existence
- `pat(pattern, value)` - Pattern matching

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

## API

### Endpoints

**Status API**:
- `GET /api/v1/endpoints/statuses` - All endpoint statuses
- `GET /api/v1/endpoints/{key}/statuses` - Single endpoint status

**Health API**:
- `GET /health` - Gatus health check

**Metrics API**:
- `GET /metrics` - Prometheus metrics export

**Example**:
```bash
# Get all statuses
curl https://status.example.com/api/v1/endpoints/statuses

# Get specific endpoint
curl https://status.example.com/api/v1/endpoints/production_website/statuses

# Prometheus metrics
curl https://status.example.com/metrics
```

### Response Format

**JSON structure**:
```json
{
  "key": "production_website",
  "name": "website",
  "group": "production",
  "results": [
    {
      "status": 200,
      "hostname": "example.com",
      "duration": 123456789,
      "conditionResults": [
        {
          "condition": "[STATUS] == 200",
          "success": true
        }
      ],
      "success": true,
      "timestamp": "2025-12-25T10:00:00Z"
    }
  ]
}
```

## Alerting In-Depth

### Alert Configuration

**Per-endpoint alerts**:
```yaml
endpoints:
  - name: critical-service
    url: "https://api.example.com"
    interval: 30s
    conditions:
      - "[STATUS] == 200"
    alerts:
      - type: slack
        enabled: true
        failure-threshold: 3
        success-threshold: 2
        send-on-resolved: true
        description: "Critical service is down!"

      - type: pagerduty
        enabled: true
        failure-threshold: 1
        success-threshold: 1
```

**Alert thresholds**:
- `failure-threshold`: Number of failures before alerting
- `success-threshold`: Number of successes before resolving
- Prevents flapping and false positives

**Alert descriptions**:
```yaml
alerts:
  - type: slack
    description: |
      *[ENDPOINT_NAME]* is [IF_CONDITION_PASSED]up[ELSE]down[END]
      Response time: [RESPONSE_TIME]ms
      Timestamp: [TIMESTAMP]
```

### Provider-Specific Configuration

**Slack**:
```yaml
alerting:
  slack:
    webhook-url: "${SLACK_WEBHOOK_URL}"
    default-alert:
      description: "Health check failed"
      send-on-resolved: true
      failure-threshold: 3
```

**GitHub Issues**:
```yaml
alerting:
  github:
    repository-url: "https://github.com/owner/repo"
    token: "${GITHUB_TOKEN}"
    default-alert:
      enabled: true
```

- Auto-creates issues prefixed with `alert(gatus):`
- Auto-closes when resolved (if `send-on-resolved: true`)

**Email (SMTP)**:
```yaml
alerting:
  email:
    from: "alerts@example.com"
    username: "${SMTP_USERNAME}"
    password: "${SMTP_PASSWORD}"
    host: "smtp.example.com"
    port: 587
    to: "team@example.com"
    default-alert:
      send-on-resolved: true
```

## Strengths

### Architecture & Design
1. ✅ **Configuration as code**: YAML-based, Git-friendly
2. ✅ **Stateless option**: Can run without database
3. ✅ **Go-based**: Fast, compiled, low resources
4. ✅ **Docker-native**: First-class container support
5. ✅ **Lightweight**: Small binary, minimal dependencies
6. ✅ **PostgreSQL support**: Production database option

### Developer Experience
7. ✅ **GitOps workflow**: Commit configs, version control
8. ✅ **Infrastructure as code**: Terraform-friendly
9. ✅ **Powerful conditions**: Flexible health criteria
10. ✅ **JSONPath queries**: Complex API validation
11. ✅ **Clear syntax**: Easy to understand YAML
12. ✅ **Extensive docs**: Well-documented conditions

### Features
13. ✅ **20+ alert providers**: Comprehensive integrations
14. ✅ **Prometheus metrics**: Observability built-in
15. ✅ **TLS monitoring**: Certificate expiration
16. ✅ **Domain expiration**: Domain renewal tracking
17. ✅ **GitHub issues**: Auto-create/close issues
18. ✅ **Multiple protocols**: HTTP, TCP, ICMP, DNS, SSH, etc.

### Operational
19. ✅ **Low resources**: Runs on minimal hardware
20. ✅ **Fast**: Go performance
21. ✅ **Simple deployment**: Single binary
22. ✅ **Kubernetes-ready**: Helm charts available
23. ✅ **Active development**: Regular updates

## Weaknesses

### User Experience
1. ❌ **No UI configuration**: YAML only, no web UI for setup
2. ❌ **Learning curve**: Requires YAML knowledge
3. ❌ **Manual setup**: Cannot click to add monitors
4. ❌ **Basic UI**: Simple status page, not fancy
5. ❌ **No charts**: Limited visualization vs Uptime Kuma
6. ❌ **Text-heavy**: Less visual, more technical

### Features
7. ❌ **Single status page**: No multiple pages
8. ❌ **No Docker monitoring**: Cannot monitor containers
9. ❌ **No database monitoring**: No direct DB connections
10. ❌ **Limited historical UI**: Basic uptime display
11. ❌ **No push monitoring**: No heartbeat/cron (must use external)
12. ❌ **No multi-language**: English only

### Access & Management
13. ❌ **No multi-user**: Basic auth only
14. ❌ **No RBAC**: All-or-nothing access
15. ❌ **No audit logs**: Limited change tracking
16. ❌ **No UI management**: Must edit YAML files

### API
17. ❌ **Read-only API**: Cannot create/update via API
18. ❌ **Limited API**: Mainly for status retrieval
19. ❌ **No webhook push**: Pull-based updates only

### Storage
20. ❌ **In-memory default**: Data lost on restart (unless configured)
21. ❌ **Manual migration**: No built-in backup/restore
22. ❌ **SQLite default**: Not ideal for high-scale

## Comparison with SolidPing

### Similarities

Both are:
- Self-hosted, open-source
- Support PostgreSQL
- API-oriented
- Docker-deployable
- Support HTTP, TCP, Ping, DNS monitoring
- Focus on developer experience
- Prometheus metrics export

### Gatus Advantages

1. **Configuration as code**: YAML-based vs database
2. **Stateless option**: No storage required
3. **Powerful conditions**: Declarative health criteria
4. **JSONPath queries**: Complex API validation
5. **Go performance**: Compiled binary, fast
6. **Mature (2019)**: 5+ years of development
7. **GitHub issues**: Auto-create/close on alerts
8. **20+ alert providers**: Extensive integrations
9. **Lightweight**: Minimal resources
10. **GitOps-friendly**: Version-controlled config

### SolidPing Advantages

1. **UI configuration**: Web-based setup via dash0 frontend
2. **PostgreSQL-native**: Designed for Postgres from start
3. **Heartbeat monitoring**: Built-in cron/push monitoring
4. **REST API**: Full CRUD via documented REST API
5. **Multi-tenancy**: Organization-scoped data isolation with RBAC
6. **RBAC**: User roles (admin/user/viewer)
7. **Modern UI**: Reactive dash0 frontend
8. **Historical data**: Rich time-series data with min/max/avg metrics
9. **Multiple status pages**: Per-organization status pages with sections
10. **Incident management**: Sophisticated tracking with escalation, relapse detection
11. **Domain expiration**: WHOIS-based monitoring (not available in Gatus)
12. **Distributed workers**: Multi-region monitoring with lease-based job distribution

### Feature Gaps in SolidPing

Areas where SolidPing should match Gatus:

**Must Have**:
1. **Conditions syntax**: Powerful health criteria (JSONPath, operators)
2. **Configuration as code**: YAML-based config option
3. **Stateless mode**: Run without persistent storage option
4. **Prometheus metrics**: `/metrics` endpoint
5. **GitHub/GitLab integration**: Issue creation
6. 🔄 **Certificate expiration**: TLS monitoring (type defined, not yet implemented)
7. ✅ **Domain expiration**: Domain tracking (done - WHOIS-based)
8. **Multiple alert providers**: Currently 3 (Slack, Email, Webhooks) vs Gatus 20+

**Should Have**:
1. JSON response validation (JSONPath)
2. Custom condition functions (len, has, pat)
3. Alert thresholds (failure/success counts)
4. Template alert messages
5. OIDC authentication
6. Grafana dashboards

**Nice to Have**:
1. Helm charts for Kubernetes
2. External endpoint type (script execution)
3. STARTTLS monitoring
4. SSH connectivity checks

## Use Cases

### Best For

**Gatus**:
- DevOps teams using GitOps
- Infrastructure as code workflows
- Teams needing powerful condition logic
- Kubernetes deployments
- Developers comfortable with YAML
- Teams requiring version-controlled config
- Lightweight, stateless deployments
- API validation with JSONPath
- Teams using GitHub/GitLab issues for alerts

**Not Ideal For**:
- Non-technical users (no UI config)
- Teams wanting visual dashboards
- Users needing rich historical charts
- Projects requiring Docker monitoring
- Teams needing database connection monitoring
- Users wanting click-to-configure
- Projects needing heartbeat/cron monitoring built-in
- Multi-language requirements

## Competitors

### vs Uptime Kuma
- **Gatus**: YAML config, stateless, conditions
- **Uptime Kuma**: UI config, 80k stars, beautiful UI
- **Winner**: Depends on preference (code vs UI)

### vs SolidPing
- **Gatus**: Mature, conditions, stateless
- **SolidPing**: PostgreSQL, API, multi-tenant, heartbeat
- **Winner**: SolidPing for SaaS, Gatus for GitOps

### vs Commercial
- **Gatus**: Free, self-hosted, limited features
- **BetterStack/Pingdom**: Full-featured, expensive, SaaS
- **Winner**: Gatus for self-hosted budget projects

## Migration & Integration

### Migrating TO Gatus

**From UI-based tools** (Uptime Kuma):
- Write YAML config (manual)
- No import tools available
- Must recreate monitors in YAML

**From other tools**:
- Export current config
- Convert to Gatus YAML format
- Test conditions carefully

### Migrating FROM Gatus

**Export options**:
- YAML config (already version-controlled)
- SQLite/Postgres database
- No official migration tools

**Advantages**:
- Config already in YAML (portable)
- Easy to version control
- Simple to replicate

### Backup Strategy

**Config backup**:
```bash
# Config is already in Git (best practice)
git add config/config.yaml
git commit -m "Update monitoring config"
git push

# Or copy config directory
cp -r config/ backup/config-$(date +%Y%m%d)/
```

**Database backup** (if using persistence):
```bash
# SQLite
cp data/gatus.db backup/

# PostgreSQL
pg_dump -h localhost -U gatus gatus > backup/gatus-$(date +%Y%m%d).sql
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
- ⚠️ Limited - designed as single instance
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

## Community & Support

### Official Resources

- **GitHub**: https://github.com/TwiN/gatus
- **Website**: https://gatus.io
- **Docs**: https://gatus.io/docs
- **Demo**: https://status.twin.sh (Gatus monitoring itself)

### Community

- **GitHub Issues**: Active issue tracker
- **Discussions**: GitHub Discussions for Q&A
- **Pull Requests**: Community contributions welcome
- **Stars**: 6,500+ (growing)

### Support Model

- **Free**: Community support via GitHub
- **Documentation**: Comprehensive docs at gatus.io
- **No paid support**: Open-source only
- **Active maintainer**: Responsive to issues

## Conclusion

Gatus is a **powerful, developer-centric monitoring tool** that excels at configuration-as-code, GitOps workflows, and flexible condition-based monitoring. It's lightweight, fast, and perfect for teams that prefer YAML over UI configuration.

### When to Choose Gatus

✅ **Choose Gatus if**:
- You prefer configuration as code (YAML)
- You use GitOps workflows
- You need powerful condition syntax (JSONPath)
- You value lightweight, stateless deployment
- You want version-controlled monitoring config
- You're comfortable editing YAML files
- You need GitHub/GitLab issue integration
- You want Prometheus metrics
- You're deploying on Kubernetes

❌ **Avoid Gatus if**:
- You want UI-based configuration
- You need beautiful visual dashboards
- You want Docker container monitoring
- You need rich historical charts
- You require database connection monitoring
- You want heartbeat/cron monitoring built-in
- You need multi-language support
- Your team prefers click-to-configure

### For SolidPing

Gatus demonstrates that **configuration as code is highly valuable** for DevOps teams. SolidPing should:

**Learn from Gatus**:
1. Add YAML-based configuration option (alongside UI)
2. Implement powerful condition syntax with JSONPath
3. Support stateless mode (optional)
4. Add Prometheus metrics endpoint
5. GitHub/GitLab issue integration
6. Alert thresholds (failure/success counts)
7. Lightweight deployment option

**Differentiate from Gatus**:
1. Provide UI configuration (in addition to YAML)
2. Rich visual dashboards and charts
3. Built-in heartbeat/cron monitoring
4. Docker container monitoring
5. Database connection monitoring
6. Multi-tenancy and RBAC
7. Multiple status pages

**Result**: Combine Gatus's GitOps-friendly approach with SolidPing's user-friendly UI to serve both DevOps teams (YAML) and traditional users (UI).

## Sources

### Official Documentation
- [Gatus GitHub Repository](https://github.com/TwiN/gatus)
- [Gatus Official Website](https://gatus.io)
- [Gatus Documentation](https://gatus.io/docs)
- [Conditions Documentation](https://gatus.io/docs/conditions)
- [Functions Documentation](https://gatus.io/docs/functions)

### Alerting
- [Getting Started with Alerting](https://gatus.io/docs/alerting-getting-started)
- [GitHub Alerting Provider](https://gatus.io/docs/alerting-github)

### Guides & Tutorials
- [Gatus Complete Guide (BrightCoding)](https://www.blog.brightcoding.dev/2025/07/26/gatus-a-complete-guide-to-self-hosted-service-monitoring-and-status-pages/)
- [Setup Gatus Guide (DB Tech Reviews)](https://dbtechreviews.com/2024/11/01/how-to-set-up-gatus-your-ultimate-open-source-website-monitoring-solution/)
- [Monitor Services with Gatus (DEV.to)](https://dev.to/smit-vaghasiya/monitor-your-services-with-gatus-docker-alternative-to-uptime-kuma-2b4m)
- [Monitor with Gatus (ComputingForGeeks)](https://computingforgeeks.com/monitor-applications-health-alerts-using-gatus/)

### Installation
- [Install Gatus with Docker Compose (Citizix)](https://citizix.com/how-to-install-and-configure-gatus-for-health-check-monitoring-using-docker-compose/)

### Comparisons
- [Gatus vs Uptime Kuma (OpenAlternative)](https://openalternative.co/compare/gatus/vs/uptime-kuma)
- [Gatus vs Uptime Kuma (Cloudpap)](https://cloudpap.com/blog/gatus-vs-uptime-kuma/)
- [Gatus vs Uptime Kuma vs UptimeRobot (SourceForge)](https://sourceforge.net/software/compare/Gatus-vs-Uptime-Kuma-vs-UptimeRobot/)
- [Replacing Uptime Kuma with Gatus](https://blog.mei-home.net/posts/k8s-migration-21-gatus/)

### Technical
- [Gatus Automated Health Dashboard](https://twin.sh/articles/46/gatus-automated-health-dashboard)
- [Gatus on IndieBase](https://indiebase.io/tools/gatus)
