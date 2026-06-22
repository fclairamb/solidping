# Checkly - Complete Analysis

## Overview

Checkly is a developer-centric monitoring platform focused on "monitoring as code" (MaC). It combines API monitoring, Playwright-based browser checks, and heartbeat monitoring with a CLI-first workflow that integrates monitoring into CI/CD pipelines. Checks are defined in code (TypeScript/JavaScript), version-controlled, and deployed alongside application code.

**Website**: https://www.checklyhq.com

**Founded**: 2018 in Berlin, Germany

**Founders**: Hannes Lenke, Tim Nolet

**Funding**: $32.25M total (Series B led by Balderton Capital, July 2024)

**Customers**: 1,000+ including Vercel, 1Password, CrowdStrike, Airbus

## Key Features

### Monitoring Types

1. **API Checks** - HTTP/HTTPS endpoint monitoring with assertions
   - Custom headers, body, authentication
   - Response validation (status, body, headers, timing)
   - Multi-step API workflows via scripts
   - Setup/teardown scripts

2. **Browser Checks** - Playwright-based real browser testing
   - Full Chrome browser execution
   - Complex user flow simulation
   - Screenshot and video capture
   - Multi-step transactions
   - Visual regression testing

3. **Heartbeat Checks** - Passive cron/job monitoring
   - Configurable periods and grace times
   - Ping URL pattern

4. **Multistep Checks** - Chained API requests
   - Share data between requests
   - Complex workflow validation

### Monitoring as Code (MaC)

**Core differentiator**: Checks defined in TypeScript/JavaScript, not UI
```typescript
// api.check.ts
import { ApiCheck, AssertionBuilder } from 'checkly/constructs'

new ApiCheck('homepage-api', {
  name: 'Homepage API',
  request: {
    url: 'https://api.example.com/health',
    method: 'GET',
    assertions: [
      AssertionBuilder.statusCode().equals(200),
      AssertionBuilder.jsonBody('$.status').equals('ok'),
    ],
  },
  runParallel: true,
})
```

**Workflow**:
1. Write checks as code in your repo
2. Run locally with `npx checkly test`
3. Deploy with `npx checkly deploy`
4. CI/CD integration (GitHub Actions, GitLab CI, etc.)

### Checkly CLI

- `npx checkly test` - Run checks locally
- `npx checkly deploy` - Deploy checks to Checkly cloud
- `npx checkly destroy` - Remove deployed checks
- `npx checkly login` - Authenticate CLI
- Local execution for development/testing
- TypeScript-first configuration

### Global Locations

**26 public locations** across:
- **Americas (6)**: US East/West, Canada, Brazil, Mexico, Chile
- **EMEA (8)**: UK, Germany, France, Netherlands, Ireland, Sweden, Italy, South Africa
- **APAC (8)**: Japan, Singapore, Australia, India, South Korea, Hong Kong, Indonesia, UAE

**Private Locations**: Deploy Checkly Agent container on your infrastructure for internal monitoring (Team plan+)

### Notification System

**17+ channels**:
- Email
- Slack
- Microsoft Teams
- Discord
- PagerDuty
- Opsgenie
- FireHydrant
- Webhooks
- SMS (via integrations)
- Phone calls (via integrations)
- Telegram
- And more via webhooks

### Advanced Features

- **OpenTelemetry integration**: Distributed tracing from checks
- **Rocky AI** (2026): AI-powered automated triage and root cause analysis
- **Terraform provider**: Infrastructure as code for check management
- **Pulumi provider**: Alternative IaC support
- **Status pages**: Public status pages
- **Parallel execution**: Run checks across locations simultaneously
- **Retries**: Configurable retry logic
- **Alert escalation**: Multi-level alerting

## Pricing

### Plans (as of March 2026)

| Plan | Price/Month | API Check Runs | Browser Check Runs | Key Features |
|------|-------------|----------------|-------------------|--------------|
| **Hobby** | Free | 10,000 | 1,000 | 1 user, basic features |
| **Starter** | $24 | 50,000 | 5,000 | 5 users, SMS alerts |
| **Team** | $64 | 200,000 | 20,000 | Private locations, SSO |
| **Enterprise** | Custom | Custom | Custom | SLA, dedicated support |

**Overage pricing**:
- API checks: $2.50 per 10,000 runs
- Browser checks: $12 per 1,000 runs

**Annual discount**: ~20% savings

### Pricing Model
- Usage-based (check runs), not monitor count
- Transparent overage pricing
- No per-seat pricing on Hobby/Starter

## Technology Stack

### Backend
- **Language**: Node.js / TypeScript
- **Database**: PostgreSQL + ClickHouse (analytics)
- **Infrastructure**: AWS

### Frontend
- **Framework**: Vue.js
- **Design**: Modern, developer-focused UI

### Check Execution
- **Browser engine**: Playwright (Chromium)
- **Runtime**: Node.js
- **Locations**: AWS regions globally

## API

### Design
- **Architecture**: RESTful
- **Authentication**: API key (Bearer token)
- **Format**: JSON
- **Documentation**: Excellent, developer-focused

### Key Endpoints
```bash
# Checks
GET /v1/checks
POST /v1/checks
GET /v1/checks/{id}
PUT /v1/checks/{id}
DELETE /v1/checks/{id}

# Check results
GET /v1/check-results/{checkId}

# Check groups
GET /v1/check-groups
POST /v1/check-groups

# Alert channels
GET /v1/alert-channels
POST /v1/alert-channels

# Dashboards
GET /v1/dashboards
```

### Developer Experience
- **CLI**: Full-featured TypeScript CLI
- **Terraform**: Official provider
- **Pulumi**: Official provider
- **GitHub Actions**: Official action
- **SDK**: JavaScript/TypeScript

## Strengths

### Developer Experience
1. ✅ **Monitoring as code**: Checks in TypeScript, version-controlled
2. ✅ **Playwright-native**: Best-in-class browser testing
3. ✅ **CLI-first**: Local testing, CI/CD integration
4. ✅ **Terraform/Pulumi**: Infrastructure as code support
5. ✅ **TypeScript-first**: Modern developer tooling

### Features
6. ✅ **Browser checks**: Full Playwright execution
7. ✅ **Multistep API checks**: Complex workflow validation
8. ✅ **OpenTelemetry**: Distributed tracing integration
9. ✅ **Private locations**: Monitor internal services
10. ✅ **Status pages**: Public status communication
11. ✅ **AI triage** (Rocky): Automated root cause analysis

### Platform
12. ✅ **26 global locations**: Good geographic coverage
13. ✅ **Parallel execution**: Multi-location checks
14. ✅ **Well-funded**: $32M+ funding, sustainable business
15. ✅ **Active development**: Frequent feature releases

## Weaknesses

### Limitations
1. ❌ **SaaS only**: No self-hosted option
2. ❌ **No ICMP/ping monitoring**: No network-level checks
3. ❌ **No TCP port monitoring**: No raw TCP checks
4. ❌ **No DNS monitoring**: No DNS record verification
5. ❌ **No SSL certificate monitoring**: No cert expiration tracking
6. ❌ **No domain expiration**: No WHOIS monitoring
7. ❌ **Code-first barrier**: Non-developers can't use it effectively
8. ❌ **Limited protocol support**: HTTP/HTTPS and heartbeat only
9. ❌ **Usage-based pricing**: Costs can be unpredictable at scale
10. ❌ **Vendor lock-in**: Proprietary platform, no data portability

### Missing for SolidPing comparison
11. ❌ **No multi-tenancy**: No organization-scoped isolation
12. ❌ **No incident management**: Basic alerting only
13. ❌ **No SMTP/IMAP monitoring**: No email protocol support

## Comparison with SolidPing

### Similarities
- Both support HTTP/HTTPS monitoring
- Both support heartbeat/cron monitoring
- Both have status pages
- Both target developer audiences

### Checkly Advantages
1. **Monitoring as code**: TypeScript-defined checks in repo
2. **Playwright browser checks**: Full browser testing
3. **CLI with local testing**: `npx checkly test`
4. **Terraform/Pulumi providers**: IaC ecosystem
5. **OpenTelemetry integration**: Distributed tracing
6. **AI triage (Rocky)**: Automated root cause analysis
7. **26 global locations**: Broader geographic coverage
8. **Multistep API checks**: Complex workflow validation
9. **CI/CD native**: GitHub Actions, GitLab CI integration
10. **Well-funded**: $32M+ ensures continued development

### SolidPing Advantages
1. **Self-hosted**: Full control, no vendor lock-in
2. **Broader protocol support**: TCP, ICMP, DNS, Domain expiration
3. **Multi-tenancy**: Organization-scoped with RBAC
4. **Incident management**: Escalation, relapse detection
5. **No usage-based pricing**: Unlimited checks (self-hosted)
6. **PostgreSQL-native**: Direct database access
7. **Privacy-first**: Data stays on your infrastructure
8. **No code required**: UI-driven check creation
9. **Distributed workers**: Deploy your own monitoring locations
10. **SSL certificate monitoring**: Planned (type defined)

### What SolidPing Should Learn

**From Checkly's developer experience**:
1. Consider monitoring-as-code option (YAML/JSON check definitions)
2. CI/CD integration for check deployment
3. OpenTelemetry trace integration
4. Terraform provider for check management
5. Multistep/chained API checks
6. Screenshot capture on failure

## Use Cases

### Best For
- **Developer teams**: Code-first monitoring workflow
- **E2E testing**: Playwright browser checks in production
- **API monitoring**: Complex API workflow validation
- **CI/CD pipelines**: Post-deployment verification
- **SaaS companies**: Monitor user-facing workflows
- **Frontend teams**: Visual regression testing

### Not Ideal For
- Non-technical users (requires coding)
- Network/infrastructure monitoring (no TCP, ICMP, DNS)
- Self-hosted requirements
- Budget-conscious teams (usage-based pricing)
- Multi-tenant platforms
- Privacy-sensitive deployments

## Sources

### Official
- [Checkly Website](https://www.checklyhq.com)
- [Checkly Documentation](https://www.checklyhq.com/docs/)
- [Checkly CLI](https://www.checklyhq.com/docs/cli/)
- [Checkly API Reference](https://www.checklyhq.com/docs/api/)
- [Checkly Pricing](https://www.checklyhq.com/pricing/)
- [Checkly Blog](https://www.checklyhq.com/blog/)

### Integrations
- [Terraform Provider](https://registry.terraform.io/providers/checkly/checkly/latest)
- [Pulumi Provider](https://www.pulumi.com/registry/packages/checkly/)
- [GitHub Action](https://github.com/marketplace/actions/checkly-cli)

**Last Updated**: 2026-03-22
