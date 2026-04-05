# Uptime Kuma - Complete Analysis

## Overview

Uptime Kuma is a fancy, self-hosted monitoring tool created by Louis Lam in 2021. It has become the most popular open-source uptime monitoring solution with over 80,000+ GitHub stars, making it the clear community favorite for self-hosted monitoring.

**GitHub**: https://github.com/louislam/uptime-kuma

**License**: MIT

**Technology**: Node.js, Vue.js, Socket.IO

**Database**: SQLite (primary), with official MariaDB support (v2.0+)

**Current Version**: 2.1.3 (as of March 2026)

## Key Statistics

- **GitHub Stars**: 83,000+ (Mar 2026)
- **Forks**: 7,100+
- **Releases**: 120+ versions
- **Contributors**: 300+ community members
- **Language Support**: 20+ languages
- **Active Development**: Very active (2021-present)

## Key Features

### Core Monitoring Capabilities

**Monitor Types Supported**:
1. **HTTP/HTTPS** - Website and API monitoring
2. **TCP Port** - Port connectivity checks
3. **Keyword** - HTTP(S) keyword presence/absence
4. **JSON Query** - HTTP(S) JSON response validation
5. **WebSocket** - WebSocket connection monitoring
6. **Ping (ICMP)** - Network ping monitoring
7. **DNS Record** - DNS resolution validation
8. **Push** - Reverse monitoring (heartbeat)
9. **Steam Game Server** - Gaming server monitoring
10. **Docker Container** - Container health checks
11. **Database** - Direct database connection monitoring
12. **Domain Expiration** - WHOIS-based domain expiry monitoring (v2.1+)

**Notable**: 12 monitor types covering web, network, protocol, and specialized monitoring

### Recent Additions (v2.0-v2.1)
- **v2.0** (Oct 2025): MariaDB support, rootless Docker images, refreshed UI
- **v2.1** (Feb 2026): Globalping support for worldwide probes, domain expiry monitoring, SSL/STARTTLS options for TCP monitors, multi-number notification support

### Monitoring Features

- **Ultra-short intervals**: 20-second minimum check interval
- **SSL/TLS monitoring**: Certificate expiration tracking
- **Keyword matching**: Search for text in responses
- **JSON validation**: JSONPath queries on API responses
- **Multi-language**: Official support for 20+ languages
- **Ping charts**: Historical ping latency visualization
- **Certificate monitoring**: SSL expiration alerts
- **2FA support**: Two-factor authentication for dashboard
- **Proxy support**: Monitor through HTTP/SOCKS proxies

### Status Pages

- **Multiple status pages**: Create separate pages for different services
- **Custom domains**: Map status pages to domains
- **Public/Private**: Control access to status pages
- **Real-time updates**: Instant status page updates via WebSocket
- **Custom branding**: Limited customization options
- **Responsive design**: Mobile-friendly status pages

### Notification System

**90+ notification services** supported via:
- **Direct integrations**: Telegram, Discord, Gotify, Slack, Pushover, Email (SMTP)
- **Apprise integration**: 78+ additional services through Apprise library

**Popular notification channels**:
- Email (SMTP)
- Slack
- Discord
- Telegram
- Microsoft Teams
- Signal
- Pushover
- Gotify
- Splunk
- SendGrid
- Twilio
- PagerDuty
- Webhook (custom)

### User Interface

- **"Fancy, Reactive, Fast UI/UX"** - Modern Vue 3 interface
- **Real-time updates**: WebSocket-based live updates
- **Dark mode**: Built-in dark theme
- **Responsive design**: Works on mobile and desktop
- **Dashboard**: Monitor group organization
- **Ping charts**: Visual latency graphs
- **Certificate info**: SSL details in UI
- **Tag system**: Organize monitors with tags

## Technology Stack

### Frontend
- **Framework**: Vue 3
- **Build Tool**: Vite.js
- **UI Library**: Bootstrap 5
- **Real-time**: Socket.IO client
- **Languages**: Vue (42.7%), JavaScript (54.8%)

### Backend
- **Runtime**: Node.js (≥20.4 required)
- **Communication**: Socket.IO (WebSocket) instead of REST API
- **Database**: SQLite (default)
- **Process Management**: PM2 (recommended for non-Docker)
- **Languages**: JavaScript, TypeScript (1.2%)

### Infrastructure
- **Container**: Docker support with Docker Compose
- **Persistence**: Volume mapping for data directory
- **Reverse Proxy**: Nginx, Apache, Traefik compatible
- **Authentication**: Built-in user system with 2FA

## Installation & Deployment

### Docker (Recommended)

**Quick Start**:
```bash
docker run -d \
  --restart=always \
  -p 3001:3001 \
  -v uptime-kuma:/app/data \
  --name uptime-kuma \
  louislam/uptime-kuma:2
```

**Docker Compose**:
```yaml
version: '3.8'
services:
  uptime-kuma:
    image: louislam/uptime-kuma:2
    container_name: uptime-kuma
    volumes:
      - ./uptime-kuma-data:/app/data
    ports:
      - 3001:3001
    restart: always
```

### Non-Docker Installation

**Requirements**:
- Node.js 20.4 or higher
- npm or pnpm
- Linux, Windows 10+, or compatible OS

**Installation**:
```bash
# Clone repository
git clone https://github.com/louislam/uptime-kuma.git
cd uptime-kuma

# Install dependencies
npm ci --production

# Run server
node server/server.js

# Or with PM2 for background operation
pm2 start server/server.js --name uptime-kuma
```

### Reverse Proxy Configuration

**Critical Requirement**: WebSocket support needed

**Nginx Example**:
```nginx
location / {
    proxy_pass http://localhost:3001;
    proxy_http_version 1.1;
    proxy_set_header Upgrade $http_upgrade;
    proxy_set_header Connection "Upgrade";
    proxy_set_header Host $host;
}
```

## API & Integration

### API Architecture

**Type**: Socket.IO-based (not REST)
- **Real-time**: WebSocket communication
- **Internal API**: Primarily for Uptime Kuma's own use
- **Not officially supported**: No stable public API guarantee
- **Breaking changes**: Possible between versions

### API Endpoints

**Limited REST endpoints**:
1. **Push monitors**: `/api/push/{push_key}` - Receive heartbeat pings
2. **Status badges**: `/api/badge/{monitor_id}/status` - SVG badges
3. **Prometheus metrics**: `/metrics` - Prometheus export
4. **Public status pages**: JSON data for public pages

**Example - Push Monitor**:
```bash
# Send heartbeat
curl https://your-kuma.com/api/push/your_push_key?status=up&msg=OK&ping=100

# Parameters:
# status: up, down
# msg: Optional message
# ping: Optional ping time in ms
```

### API Keys

- **Prometheus metrics**: API keys for authentication
- **Default**: HTTP basic authentication
- **Configuration**: Enable API keys in settings

### Third-Party Integrations

**Python Wrapper**: `uptime-kuma-api`
- **PyPI**: https://pypi.org/project/uptime-kuma-api/
- **Docs**: https://uptime-kuma-api.readthedocs.io/
- **Purpose**: Programmatic Socket.IO API access
- **Use case**: Automation, bulk monitor management

**Terraform**: Community Terraform providers available

**Home Assistant**: Official add-on available

## Configuration

### Configuration Method

**UI-based configuration**:
- All settings configured through web interface
- No YAML or file-based configuration
- Settings stored in SQLite database
- **No version control**: Config not in files

**Advantages**:
- User-friendly
- No syntax errors
- Immediate visual feedback
- Accessible to non-technical users

**Disadvantages**:
- No infrastructure as code
- No Git versioning
- Difficult to backup/restore config separately
- No programmatic configuration (without API)
- Manual replication across instances

### Database

**Default**: SQLite database in `/app/data/`
- **File**: `kuma.db`
- **Advantages**: No external dependencies, simple deployment
- **Disadvantages**: Single-file risks, limited scalability

**Community Forks**:
- **MySQL/MariaDB support**: Unofficial forks available
- **PostgreSQL**: Not officially supported
- **Migration challenges**: Reported issues with SQLite corruption in v1→v2 upgrade

### Multi-Tenancy & Access Control

**Limitations**:
- **Single-user focus**: Primary limitation
- **No RBAC**: No role-based access control
- **Anyone with access can modify**: Security concern
- **No multi-tenant support**: Cannot isolate teams/customers
- **Dashboard access = full admin**: All-or-nothing access

**Workarounds**:
- Run multiple instances (one per team/customer)
- Use reverse proxy authentication
- Restrict access at network level

## Monitoring Capabilities

### Monitor Configuration

**Common Settings**:
- **Name**: Monitor display name
- **URL/Host**: Target to monitor
- **Interval**: Check frequency (20s minimum)
- **Retries**: Number of retries before marking down
- **Timeout**: Request timeout
- **Tags**: Organize monitors
- **Notifications**: Which channels to alert

**HTTP/HTTPS Specific**:
- **Method**: GET, POST, PUT, PATCH, DELETE, etc.
- **Headers**: Custom HTTP headers
- **Body**: Request body for POST/PUT
- **Expected status**: Success status codes
- **Keywords**: Search for text in response
- **Ignore TLS**: Skip SSL verification
- **Follow redirects**: Automatic redirect following

**TCP Specific**:
- **Port**: TCP port number
- **Host**: Target hostname or IP

**DNS Specific**:
- **Hostname**: Domain to resolve
- **Resolver**: DNS server to query
- **Expected record**: Verify DNS response

**Push Specific**:
- **Push URL**: Generated endpoint for heartbeat
- **Expected interval**: How often to expect pings

### Notification Configuration

**Per-Monitor Notifications**:
- Select specific notification channels
- Different alerts for different monitors
- Can have multiple channels per monitor

**Notification Settings**:
- **Up/Down alerts**: Separate control
- **Repeat notifications**: Reminder intervals
- **Default notifications**: Apply to all new monitors

### Status Page Configuration

**Multiple Pages**:
- Create separate status pages
- Map pages to domains
- Public or password-protected

**Page Settings**:
- **Title**: Status page name
- **Description**: Page description
- **Custom CSS**: Limited styling
- **Show tags**: Group monitors by tags
- **Incident history**: Show past incidents

## Strengths

### User Experience
1. ✅ **Beautiful UI**: Modern, reactive Vue 3 interface
2. ✅ **Easy setup**: Docker one-liner deployment
3. ✅ **User-friendly**: No YAML configuration needed
4. ✅ **Real-time updates**: WebSocket-based instant updates
5. ✅ **Dark mode**: Built-in dark theme
6. ✅ **Multi-language**: 20+ language support

### Features
7. ✅ **90+ notifications**: Extensive notification support via Apprise
8. ✅ **11 monitor types**: Comprehensive protocol coverage
9. ✅ **20-second intervals**: Fast check frequency
10. ✅ **Status pages**: Multiple public status pages
11. ✅ **Certificate monitoring**: SSL expiration tracking
12. ✅ **Docker monitoring**: Container health checks

### Community
13. ✅ **80k+ stars**: Largest open-source monitoring community
14. ✅ **Very active**: Frequent updates and bug fixes
15. ✅ **300+ contributors**: Strong community support
16. ✅ **MIT license**: Permissive open source
17. ✅ **Great documentation**: Active wiki and community guides

### Deployment
18. ✅ **Self-hosted**: Full control over data
19. ✅ **Docker support**: Easy containerized deployment
20. ✅ **Low resources**: Runs on minimal hardware
21. ✅ **No external dependencies**: SQLite-based

## Weaknesses

### Architecture
1. ❌ **No REST API**: Socket.IO only, no stable public API
2. ❌ **No PostgreSQL**: Only SQLite and MariaDB (v2.0+)
3. ❌ **Single-user limitation**: No multi-tenancy or RBAC
4. ❌ **No config files**: UI-only configuration (no IaC)
5. ❌ **Database corruption**: Reported SQLite issues under load
6. ❌ **No horizontal scaling**: Single instance limitation

### Configuration & Management
7. ❌ **No version control**: Config stored in database, not files
8. ❌ **No bulk operations**: Must configure monitors individually in UI
9. ❌ **No config export**: Difficult to backup/replicate config
10. ❌ **No API for config**: Can't automate monitor creation easily
11. ❌ **Manual setup**: Each instance must be configured via UI

### Access Control
12. ❌ **All-or-nothing access**: Anyone with access can modify everything
13. ❌ **No read-only users**: Cannot grant view-only access
14. ❌ **No team isolation**: Cannot separate teams/customers
15. ❌ **No audit logs**: Limited tracking of who changed what

### Enterprise Features
16. ❌ **No incident management**: Basic alerting only
17. ❌ **No on-call scheduling**: No rotation management
18. ❌ **No escalation rules**: Simple alert routing
19. ❌ **No SLA tracking**: Basic uptime percentage only
20. ⚠️ **Limited multi-location**: Globalping support added in v2.1, but not native distributed workers

## Comparison with SolidPing

### Similarities

Both are:
- Self-hosted, open-source solutions
- MIT licensed
- Docker-deployable
- Support HTTP, TCP, Ping, DNS monitoring
- Provide status pages
- Support multiple notification channels
- Focus on privacy and data ownership

### Uptime Kuma Advantages

1. **80k+ stars**: Massive community and maturity
2. **Beautiful UI**: Modern Vue 3 reactive interface
3. **90+ notifications**: Extensive Apprise integration
4. **20-second intervals**: Faster than typical 1-minute
5. **11 monitor types**: Docker, Steam, WebSocket support
6. **Multi-language**: 20+ language translations
7. **Status pages**: Multiple public status pages
8. **Easy setup**: One-line Docker deployment
9. **Active development**: Frequent updates
10. **Strong community**: 300+ contributors, active wiki

### SolidPing Advantages

1. **PostgreSQL-native**: Enterprise database vs SQLite/MariaDB
2. **REST API**: Stable, documented API vs unstable Socket.IO
3. **Multi-tenancy**: Organization-scoped data isolation with RBAC (admin/user/viewer roles)
4. **Horizontal scaling**: PostgreSQL + distributed workers with lease-based job distribution
5. **API-first**: Full CRUD via REST API with OpenAPI spec
6. **Go backend**: Performance and concurrency
7. **Incident management**: Sophisticated incident tracking with escalation, relapse detection
8. **Domain expiration**: WHOIS-based monitoring with configurable thresholds
9. **DNS monitoring**: A, AAAA, CNAME, MX, NS, TXT record support
10. **Professional architecture**: Enterprise-ready design with clean handler-service pattern

### Feature Gaps in SolidPing

Areas where SolidPing should match/exceed Uptime Kuma:

**Must Have** (Uptime Kuma has):
1. Beautiful reactive UI (SolidPing has dash0 frontend)
2. Real-time updates (WebSocket/SSE)
3. Docker container monitoring
4. WebSocket protocol monitoring
5. Steam game server monitoring (optional)
6. ✅ Push/heartbeat monitoring (done)
7. ✅ Multiple status pages (done)
8. 🔄 Certificate expiration alerts (type defined, not yet implemented)
9. Ping charts and historical graphs (SolidPing has response time metrics)
10. ✅ Check groups for organization (done)

**Should Have**:
1. 90+ notification integrations (via Apprise-like approach)
2. Multi-language support
3. Dark mode UI
4. Status page domain mapping
5. Easy Docker deployment (have this)

**Nice to Have**:
1. JSON query validation (JSONPath)
2. Database connection monitoring
3. 20-second check intervals
4. Custom CSS for status pages

## Use Cases

### Best For

**Uptime Kuma**:
- Hobbyists and personal projects
- Small teams without complex needs
- Users prioritizing ease of use over flexibility
- Projects not needing multi-tenancy
- Single-administrator setups
- Users comfortable with UI-based config
- Teams needing extensive notification options
- Docker-heavy environments

**Not Ideal For**:
- Multi-tenant SaaS platforms
- Large enterprises needing RBAC
- Teams requiring config version control
- High-scale deployments (SQLite limits)
- Infrastructure-as-code workflows
- Programmatic monitor management
- Teams needing API-first approach
- Horizontally scaled deployments

## Competitors

### Direct Competitors (Self-Hosted)
- **Gatus**: YAML-config, lightweight, conditions-based
- **SolidPing**: PostgreSQL, API-first, multi-tenant ready
- **Statping-ng**: Mature, Go-based
- **Healthchecks.io**: Cron monitoring focus

### Why Users Choose Uptime Kuma
1. Largest community (80k stars)
2. Easiest setup (Docker one-liner)
3. Best-looking UI
4. Most notification options (90+)
5. Frequent updates
6. Multi-language support
7. No learning curve

### Why Users Choose Alternatives
1. **Gatus**: YAML config, no database needed
2. **SolidPing**: PostgreSQL, API, multi-tenancy
3. **Statping-ng**: More mature, stable
4. **Healthchecks.io**: Better cron monitoring

## Migration & Integration

### Migrating TO Uptime Kuma

**From other tools**:
- Manual monitor recreation (no bulk import)
- Use Python API wrapper for automation
- No built-in migration tools

**Challenges**:
- UI-based configuration (no import)
- Must recreate notification settings
- No config file to import

### Migrating FROM Uptime Kuma

**Export options**:
- SQLite database backup
- Python API wrapper to extract config
- Manual export via screenshots/documentation

**Challenges**:
- No official export format
- Notifications must be recreated
- Status pages must be rebuilt

### Backup Strategy

**Critical files**:
```
/app/data/
├── kuma.db        # Main database
├── upload/        # Uploaded files
└── config/        # App config
```

**Backup approach**:
```bash
# Stop container
docker stop uptime-kuma

# Backup data volume
docker run --rm \
  -v uptime-kuma:/data \
  -v $(pwd):/backup \
  alpine tar czf /backup/kuma-backup.tar.gz /data

# Restart
docker start uptime-kuma
```

## Performance & Scalability

### Resource Requirements

**Minimum**:
- **CPU**: 1 core
- **RAM**: 512 MB
- **Disk**: 1 GB (for database)

**Recommended**:
- **CPU**: 2 cores
- **RAM**: 1-2 GB
- **Disk**: 5 GB

### Scalability Limits

**SQLite constraints**:
- **Concurrent writes**: Limited (single writer)
- **Database size**: Practical limit ~100 GB
- **Corruption risk**: Under heavy load
- **No replication**: Single file database

**Horizontal scaling**:
- ❌ Not supported
- ❌ Cannot run multiple instances sharing database
- ⚠️ Must run separate instances with separate databases

**Monitoring capacity**:
- Can handle 100-500 monitors comfortably
- Performance degrades with 1000+ monitors
- Depends on check intervals and database size

### Performance Tips

1. Use longer check intervals for non-critical monitors
2. Limit notification channels per monitor
3. Regular database vacuum/optimization
4. Monitor on SSD storage
5. Keep database size manageable
6. Use Docker for better resource isolation

## Security Considerations

### Authentication

**Built-in auth**:
- Username/password system
- Two-factor authentication (2FA)
- Session management

**Limitations**:
- No SSO/SAML support
- No LDAP integration
- No OAuth providers

### Authorization

**Access control**:
- ❌ No granular permissions
- ❌ All authenticated users are admins
- ⚠️ Anyone with access can delete monitors

**Workarounds**:
- Reverse proxy authentication
- Network-level access control
- VPN requirement

### Data Security

- SQLite database unencrypted by default
- HTTPS recommended for dashboard access
- Secrets stored in database (plaintext)
- No built-in encryption at rest

## Community & Support

### Official Resources

- **GitHub**: https://github.com/louislam/uptime-kuma
- **Wiki**: https://github.com/louislam/uptime-kuma/wiki
- **Demo**: https://demo.uptime.kuma.pet (10-min sessions)
- **Website**: https://uptime.kuma.pet

### Community

- **GitHub Discussions**: Active community Q&A
- **Issue Tracker**: 1000+ open issues, responsive maintainer
- **Pull Requests**: Accepting community contributions
- **Reddit**: r/UptimeKuma subreddit

### Support Model

- **Free**: Community support only
- **GitHub Issues**: Bug reports and feature requests
- **Wiki**: Comprehensive documentation
- **Community guides**: Better Stack, blog posts

## Conclusion

Uptime Kuma is the **most popular self-hosted monitoring solution** (80k+ stars) for good reason: it's beautiful, easy to use, feature-rich, and actively maintained. However, it's designed for simplicity and ease-of-use rather than enterprise features, scalability, or API-first workflows.

### When to Choose Uptime Kuma

✅ **Choose Uptime Kuma if**:
- You want the easiest setup (Docker one-liner)
- You prefer UI-based configuration
- You need extensive notification options (90+)
- You value beautiful, modern UI
- You're running a single team/customer
- You don't need multi-tenancy or RBAC
- You're comfortable with SQLite
- You want the largest community support

❌ **Avoid Uptime Kuma if**:
- You need multi-tenancy or RBAC
- You require PostgreSQL/MySQL
- You want infrastructure as code (YAML config)
- You need a stable public REST API
- You're building a SaaS platform
- You need horizontal scaling
- You require config version control
- You need programmatic bulk operations

### For SolidPing

Uptime Kuma demonstrates that **user experience matters enormously**—its 80k stars prove that a beautiful UI and ease of use can dominate the market despite architectural limitations. SolidPing should:

**Learn from Uptime Kuma**:
1. Prioritize UI/UX (reactive, real-time, beautiful)
2. Make deployment trivially easy (Docker one-liner)
3. Support many notification channels
4. Add visual monitoring (ping charts, graphs)
5. Multi-language support
6. Certificate monitoring

**Differentiate from Uptime Kuma**:
1. PostgreSQL-native (enterprise database)
2. REST API-first (stable, documented)
3. Multi-tenancy and RBAC (enterprise-ready)
4. Config as code (YAML + UI)
5. Horizontal scalability
6. Professional architecture

**Result**: Combine Uptime Kuma's ease-of-use with SolidPing's enterprise architecture to create the best of both worlds.

## Sources

### Official Documentation
- [Uptime Kuma GitHub](https://github.com/louislam/uptime-kuma)
- [Uptime Kuma Website](https://uptime.kuma.pet)
- [Uptime Kuma Wiki](https://github.com/louislam/uptime-kuma/wiki)
- [Uptime Kuma Demo](https://demo.uptime.kuma.pet)

### Guides & Tutorials
- [Complete Guide to Uptime Kuma (Better Stack)](https://betterstack.com/community/guides/monitoring/uptime-kuma-guide/)
- [Uptime Kuma Installation Guide](https://github.com/louislam/uptime-kuma/wiki/🔧-How-to-Install)
- [Status Page Documentation](https://github.com/louislam/uptime-kuma/wiki/Status-Page)
- [Notification Methods](https://github.com/louislam/uptime-kuma/wiki/Notification-Methods)

### API & Integration
- [API Documentation](https://github.com/louislam/uptime-kuma/wiki/API-Documentation)
- [API Keys](https://github.com/louislam/uptime-kuma/wiki/API-Keys)
- [Python API Wrapper](https://uptime-kuma-api.readthedocs.io/)
- [Python API on PyPI](https://pypi.org/project/uptime-kuma-api/)

### Comparisons & Reviews
- [Uptime Kuma vs Gatus (OpenAlternative)](https://openalternative.co/compare/gatus/vs/uptime-kuma)
- [Uptime Kuma vs Gatus (Cloudpap)](https://cloudpap.com/blog/gatus-vs-uptime-kuma/)
- [Top Uptime Kuma Alternatives (Better Stack)](https://betterstack.com/community/comparisons/uptime-kuma-alternative/)
- [Uptime Kuma vs Gatus (Slashdot)](https://slashdot.org/software/comparison/Gatus-vs-Uptime-Kuma/)
