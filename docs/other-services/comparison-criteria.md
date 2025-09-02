# Comparison Criteria for Uptime Monitoring Services

Comprehensive list of evaluation criteria used to analyze and compare uptime monitoring services.

## 1. Pricing & Business Model

### 1.1 Free Tier
- **Free tier availability**: Yes/No
- **Free monitors count**: Number of monitors included
- **Free check interval**: Minimum check frequency (e.g., 1-min, 5-min)
- **Free tier limitations**: SMS limits, API limits, feature restrictions
- **Free tier duration**: Permanent vs trial period
- **Credit card requirement**: Required for free tier signup

**Scoring**:
- 🥇 Best: 50+ monitors, 5-min or better, no credit card
- 🥈 Good: 10-50 monitors, 5-min checks
- 🥉 Fair: 1-10 monitors or trial only
- ❌ Poor: No free tier

### 1.2 Paid Plans
- **Entry price**: Lowest paid tier cost per month
- **Monitor pricing**: Cost per monitor or fixed tiers
- **Price scaling**: Linear vs tiered vs usage-based
- **Annual discount**: Savings for annual billing
- **Enterprise pricing**: Custom quotes vs transparent
- **Free trial**: Duration without credit card

**Key Metrics**:
- $ per 10 monitors
- $ per 50 monitors
- $ per 100 monitors
- $ per 1000 monitors

### 1.3 Additional Costs
- **SMS alerts**: Included vs quota vs per-SMS
- **Voice calls**: Included vs quota vs per-call
- **Advanced monitors**: Transaction/browser tests cost
- **Team members**: Per-user pricing vs unlimited
- **API calls**: Rate limits and overage costs
- **Data retention**: Historical data storage costs
- **Support costs**: Premium support pricing

### 1.4 Business Model
- **Pricing model**: Monitor-based / User-based / Feature-based / Hybrid
- **Billing flexibility**: Monthly / Annual / Usage-based
- **Refund policy**: Money-back guarantee period
- **Price predictability**: Fixed vs variable costs
- **Hidden fees**: Setup, overage, addon costs

## 2. Monitor Types & Protocols

### 2.1 Web Monitoring
- **HTTP/HTTPS**: GET, POST, PUT, PATCH, DELETE support
- **HTTP Custom**: Server-side script integration
- **Keyword monitoring**: Presence/absence detection
- **Expected status codes**: Custom success codes
- **Custom headers**: Request header customization
- **Request body**: POST/PUT data support
- **Authentication**: Basic, Digest, Bearer token support
- **SSL verification**: Option to ignore SSL errors
- **Follow redirects**: Automatic redirect following

### 2.2 Network Monitoring
- **Ping (ICMP)**: Standard ICMP ping checks
- **TCP port**: TCP connectivity testing
- **UDP port**: UDP protocol support
- **DNS monitoring**: DNS resolution verification

### 2.3 Email Protocols
- **SMTP**: Mail server monitoring
- **POP3/POP3S**: POP3 protocol support
- **IMAP/IMAPS**: IMAP protocol support

### 2.4 Advanced Monitoring
- **Heartbeat/Cron**: Reverse monitoring for scheduled jobs
- **Transaction/Browser**: Real browser testing (Selenium/Playwright)
- **Page speed**: Load time and performance analysis
- **Real User Monitoring (RUM)**: Actual visitor analytics
- **API monitoring**: Specialized API testing
- **SSL certificate**: Certificate expiration tracking
- **Domain expiration**: Domain renewal monitoring

### 2.5 Specialized Checks
- **Multi-step transactions**: Complex user flows
- **WebSocket monitoring**: Real-time protocol support
- **GraphQL monitoring**: GraphQL-specific checks
- **FTP/SFTP**: File transfer protocol monitoring
- **SNMP**: Network device monitoring
- **Database monitoring**: Direct DB connection checks

**Scoring**:
- Essential: HTTP, TCP, Ping, Heartbeat
- Important: DNS, SMTP, SSL monitoring
- Advanced: Transaction, RUM, Page speed
- Specialized: WebSocket, GraphQL, SNMP

## 3. Monitoring Configuration

### 3.1 Check Intervals
- **Minimum interval**: Fastest check frequency
- **Free tier interval**: Free plan check frequency
- **Interval options**: Available check frequencies
- **Adaptive intervals**: Dynamic frequency adjustment

**Benchmarks**:
- 30 seconds: Premium
- 1 minute: Standard
- 5 minutes: Budget/Free tier
- 15+ minutes: Legacy/Basic

### 3.2 Timeout Settings
- **Default timeout**: Standard timeout duration
- **Configurable timeout**: Customizable per check
- **Timeout range**: Min/max timeout values

### 3.3 Advanced Options
- **Retry logic**: Automatic retry on failure
- **Confirmation period**: Wait before alerting
- **Recovery period**: Wait before marking up
- **False positive reduction**: Multi-location verification
- **Grace periods**: Buffer time for heartbeats

### 3.4 Geographic Coverage
- **Probe locations**: Number of monitoring locations
- **Region selection**: Choose specific regions
- **Multi-location checks**: Checks from multiple locations
- **Geo-verification**: Multiple location confirmation
- **Custom locations**: Private probe deployment

**Scoring**:
- 🥇 100+ locations: Excellent
- 🥈 50-100 locations: Very good
- 🥉 10-50 locations: Good
- ⚠️ <10 locations: Limited

## 4. Alerting & Notifications

### 4.1 Notification Channels
- **Email**: Standard email alerts
- **SMS**: Text message notifications
- **Voice calls**: Phone call alerts
- **Push notifications**: Mobile app alerts
- **Webhooks**: Custom HTTP callbacks
- **Slack**: Native Slack integration
- **Microsoft Teams**: Teams integration
- **Discord**: Discord notifications
- **Telegram**: Telegram bot support
- **PagerDuty**: PagerDuty integration
- **Opsgenie**: Opsgenie integration
- **VictorOps/Splunk On-Call**: Integration support
- **Custom integrations**: API for custom channels

### 4.2 Alert Configuration
- **Alert routing**: Different alerts for different monitors
- **Escalation rules**: Progressive alert escalation
- **Alert schedules**: Time-based alert routing
- **Alert suppression**: Temporary muting
- **Alert grouping**: Batch similar alerts
- **Alert acknowledgment**: Mark alerts as seen
- **Alert snoozing**: Temporary silence

### 4.3 Alert Limits
- **SMS quotas**: Included SMS messages
- **Voice call limits**: Included phone calls
- **Alert frequency**: Cooldown between alerts
- **All-you-can-alert**: Unlimited alerting

### 4.4 On-Call Management
- **On-call schedules**: Rotation management
- **Team escalation**: Escalate to team
- **Incident assignment**: Automatic assignment
- **Override support**: Temporary schedule changes

**Scoring**:
- 🥇 10+ channels, unlimited alerts: Excellent
- 🥈 5-10 channels, generous limits: Very good
- 🥉 3-5 channels, reasonable limits: Good
- ❌ Limited channels, restrictive quotas: Poor

## 5. API & Developer Experience

### 5.1 API Design
- **Architecture**: RESTful / GraphQL / RPC
- **API specification**: OpenAPI / JSON:API / Custom
- **HTTP methods**: GET, POST, PUT, PATCH, DELETE support
- **Response format**: JSON / XML / Multiple
- **Request format**: JSON / Form / Multiple
- **Versioning**: API version strategy
- **Backward compatibility**: Breaking change handling

### 5.2 Authentication
- **Auth method**: Bearer token / API key / OAuth2 / Basic
- **Token types**: Account-wide / Read-only / Scoped
- **Token management**: Creation, rotation, revocation
- **Security**: Token encryption, HTTPS requirement
- **Multi-factor auth**: 2FA for dashboard access

### 5.3 API Capabilities
- **Full CRUD**: Create, Read, Update, Delete for all resources
- **Bulk operations**: Batch create/update/delete
- **Filtering**: Query parameter filtering
- **Sorting**: Result ordering
- **Pagination**: Offset / Cursor / Page-based
- **Search**: Full-text search support
- **Rate limiting**: Request quotas
- **Webhooks**: Event-driven notifications

### 5.4 Documentation
- **Quality**: Completeness and clarity
- **Examples**: Code samples in multiple languages
- **Interactive docs**: Swagger / Postman / Try-it interface
- **SDKs**: Official client libraries
- **Accessibility**: Works without JavaScript
- **API changelog**: Version history and updates

### 5.5 Rate Limits
- **Free tier limits**: Requests per minute/hour
- **Paid tier limits**: Scaling with plan
- **Rate limit headers**: X-RateLimit-* headers
- **Overage handling**: Throttling vs blocking vs billing
- **Documentation**: Clear limit documentation

**Scoring**:
- 🥇 Excellent docs, generous limits, SDKs: Excellent
- 🥈 Good docs, clear limits, examples: Very good
- 🥉 Basic docs, some limits, working API: Good
- ❌ Poor/JS-required docs, unclear limits: Poor

## 6. Features & Functionality

### 6.1 Status Pages
- **Public status pages**: Customer-facing pages
- **Custom domains**: status.yourdomain.com
- **Custom branding**: Logo, colors, CSS
- **Password protection**: Private status pages
- **Subscriber management**: Email notification signups
- **Incident posting**: Manual incident updates
- **Scheduled maintenance**: Planned downtime notices
- **Historical uptime**: Past performance display
- **Multiple pages**: Separate pages per service/customer

### 6.2 Reporting & Analytics
- **Uptime percentage**: SLA calculations
- **Response time charts**: Performance graphs
- **Availability reports**: Historical availability
- **Incident reports**: Downtime analysis
- **Performance trends**: Long-term analytics
- **Custom date ranges**: Flexible reporting periods
- **Export capabilities**: PDF, CSV, JSON export
- **Scheduled reports**: Automated report delivery

### 6.3 Incident Management
- **Automatic detection**: Smart incident creation
- **Incident merging**: Combine related incidents
- **Root cause analysis**: Automated diagnostics
- **Incident timeline**: Event chronology
- **Post-mortem**: Incident analysis tools
- **Incident API**: Programmatic incident management

### 6.4 Maintenance Windows
- **Scheduled maintenance**: Plan downtime
- **Recurring windows**: Regular maintenance periods
- **Alert suppression**: No alerts during maintenance
- **Automatic scheduling**: Calendar integration

### 6.5 Team Collaboration
- **Multi-user support**: Team accounts
- **Role-based access**: Admin / User / Read-only
- **Activity logs**: Audit trail
- **Comments/notes**: Collaboration features
- **Team notifications**: Group alerts

### 6.6 Integrations
- **Third-party tools**: Slack, Teams, PagerDuty, etc.
- **Terraform provider**: Infrastructure as code
- **CI/CD integration**: GitHub Actions, GitLab CI
- **Zapier**: Workflow automation
- **API webhooks**: Custom integrations

### 6.7 Advanced Diagnostics
- **Traceroute**: Network path analysis
- **MTR reports**: My TraceRoute diagnostics
- **Screenshot capture**: Visual error recording
- **HAR files**: HTTP Archive for debugging
- **DNS lookup**: DNS resolution details
- **SSL details**: Certificate information

## 7. Platform & Infrastructure

### 7.1 Deployment Model
- **SaaS only**: Cloud-hosted only
- **Self-hosted**: On-premises deployment
- **Hybrid**: Both options available
- **Open source**: Source code available

### 7.2 Technology Stack
- **Backend language**: Go, Python, Node.js, etc.
- **Database**: PostgreSQL, MySQL, SQLite, etc.
- **Frontend**: React, Vue, Angular, etc.
- **Infrastructure**: Docker, Kubernetes support
- **Scalability**: Horizontal scaling support

### 7.3 Data & Privacy
- **Data location**: US, EU, multi-region
- **GDPR compliance**: EU data protection
- **Data retention**: Historical data storage period
- **Data ownership**: Who owns the monitoring data
- **Data export**: Ability to export all data
- **Privacy policy**: Data usage transparency

### 7.4 Reliability & Performance
- **Uptime SLA**: Service level agreement
- **API response time**: Average API latency
- **Check reliability**: False positive rate
- **Multi-region**: Geographic redundancy
- **Status page**: Own service status page

### 7.5 Mobile Applications
- **iOS app**: Native iPhone/iPad app
- **Android app**: Native Android app
- **App features**: View checks, alerts, acknowledge incidents
- **Push notifications**: Mobile alert delivery

## 8. Support & Resources

### 8.1 Customer Support
- **Support channels**: Email, Chat, Phone
- **Support hours**: 24/7 vs business hours
- **Response time**: Average reply time
- **Support tiers**: Free vs paid support
- **Priority support**: Available for paid plans
- **Account manager**: Dedicated support person

### 8.2 Documentation
- **Getting started**: Onboarding guides
- **User guides**: Feature documentation
- **API docs**: Technical documentation
- **Video tutorials**: Video content
- **Blog**: Educational content
- **Knowledge base**: Searchable help center

### 8.3 Community
- **Community forum**: User discussions
- **GitHub presence**: Open source repos
- **Discord/Slack**: Community chat
- **Social media**: Active presence
- **User feedback**: Feature request process

## 9. Competitive Factors

### 9.1 Market Position
- **Company age**: Years in business
- **User base**: Number of active users
- **Market share**: Position in market
- **Ownership**: Independent vs acquired
- **Funding**: Bootstrap vs VC-backed
- **Profitability**: Financial stability

### 9.2 Reputation
- **User reviews**: G2, Capterra, Trustpilot ratings
- **False positive rate**: Reliability complaints
- **Customer satisfaction**: NPS score
- **Brand recognition**: Industry awareness
- **Thought leadership**: Blog, content quality

### 9.3 Innovation
- **Feature velocity**: New feature release frequency
- **Modern tech**: Use of current technologies
- **Developer focus**: API-first approach
- **Open source contributions**: Community involvement

## 10. Value Proposition

### 10.1 Best For
- **Hobbyists**: Personal projects
- **Startups**: Early-stage companies
- **SMBs**: Small/medium businesses
- **Enterprises**: Large organizations
- **Developers**: Technical users
- **Agencies**: Multi-client management
- **DevOps teams**: Operations focus

### 10.2 Use Cases
- **Simple uptime**: Basic website monitoring
- **Complex monitoring**: Multi-protocol, multi-location
- **API monitoring**: REST, GraphQL APIs
- **Cron monitoring**: Scheduled job tracking
- **Performance monitoring**: Page speed, RUM
- **Infrastructure**: Server and network monitoring
- **Compliance**: Audit and SLA requirements

### 10.3 Strengths & Weaknesses
- **Core strengths**: Primary advantages
- **Key weaknesses**: Main limitations
- **Differentiators**: Unique selling points
- **Gaps**: Missing features vs competitors

## 11. Migration & Lock-In

### 11.1 Vendor Lock-In
- **Data portability**: Easy export
- **API access**: Programmatic data access
- **Standard formats**: Open data formats
- **Migration tools**: Import/export utilities
- **Contract terms**: Cancellation policies

### 11.2 Getting Started
- **Setup time**: Time to first monitor
- **Learning curve**: Ease of use
- **Onboarding**: Guided setup process
- **Import options**: Bulk import capabilities

## Scoring Framework

### Overall Score Calculation

Each category weighted by importance:

1. **Pricing** (20%): Free tier, cost efficiency
2. **Features** (20%): Monitor types, capabilities
3. **API Quality** (15%): Documentation, design
4. **Reliability** (15%): Uptime, false positives
5. **Alerting** (10%): Channels, flexibility
6. **Support** (10%): Documentation, help
7. **Innovation** (5%): Modern features, updates
8. **Platform** (5%): Infrastructure, deployment

### Rating Scale

- ⭐⭐⭐⭐⭐ (5/5): Exceptional, industry-leading
- ⭐⭐⭐⭐ (4/5): Excellent, highly recommended
- ⭐⭐⭐ (3/5): Good, solid choice
- ⭐⭐ (2/5): Fair, acceptable with caveats
- ⭐ (1/5): Poor, significant issues

## Comparison Table Template

| Criteria | Weight | Service A | Service B | Service C |
|----------|--------|-----------|-----------|-----------|
| Free monitors | 5% | 50 ⭐⭐⭐⭐⭐ | 10 ⭐⭐⭐ | 0 ⭐ |
| Entry price | 5% | $7 ⭐⭐⭐⭐⭐ | $18 ⭐⭐⭐ | $10 ⭐⭐⭐⭐ |
| Monitor types | 10% | 8 types ⭐⭐⭐⭐ | 12 types ⭐⭐⭐⭐⭐ | 9 types ⭐⭐⭐⭐ |
| Check interval | 5% | 30s ⭐⭐⭐⭐⭐ | 1min ⭐⭐⭐⭐ | 1min ⭐⭐⭐⭐ |
| API quality | 15% | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐ |
| Alerting | 10% | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ |
| Reliability | 15% | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐ |
| Documentation | 10% | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐ |
| Features | 10% | ⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐⭐ |
| Support | 10% | ⭐⭐⭐⭐ | ⭐⭐⭐⭐ | ⭐⭐⭐ |
| Innovation | 5% | ⭐⭐⭐⭐ | ⭐⭐⭐⭐⭐ | ⭐⭐⭐ |
| **Total** | 100% | **4.3/5** | **4.5/5** | **2.8/5** |

## Quick Decision Matrix

### By Budget

| Budget | Recommended | Alternative |
|--------|-------------|-------------|
| $0 | UptimeRobot (50 free) | StatusCake (10 free + SMS) |
| <$10/mo | UptimeRobot ($7) | Healthchecks.io ($5, cron) |
| $10-50/mo | StatusCake (€17-70) | Checkly ($24-64) |
| $50-100/mo | BetterStack (modular) | Site24x7 |
| $100+/mo | Datadog, New Relic | Pingdom, Dynatrace |
| Self-hosted | Uptime Kuma, SolidPing | Gatus, Healthchecks.io |

### By Use Case

| Use Case | Best Choice | Why |
|----------|-------------|-----|
| Hobby projects | UptimeRobot | 50 free monitors |
| Startup | BetterStack or UptimeRobot | Features vs cost |
| Enterprise | Datadog, New Relic | Full observability |
| Cron monitoring | Healthchecks.io, Cronitor | Specialized, open source |
| API monitoring | Checkly, Assertible | Monitoring as code |
| Browser testing | Checkly | Playwright-native |
| Self-hosted | Uptime Kuma, SolidPing | Privacy, control |
| Multi-protocol | StatusCake | HTTP, TCP, DNS, SMTP, SSH, SSL |
| Status pages | Statuspage, Instatus | Dedicated solution |
| Performance | Datadog, SpeedCurve | RUM, page speed |

## Evaluation Checklist

When evaluating a new monitoring service:

- [ ] Check free tier: monitors, interval, duration
- [ ] Review pricing: entry price, scaling, hidden costs
- [ ] Test API: documentation, examples, rate limits
- [ ] Verify monitor types: protocols needed
- [ ] Test alerting: channels, limits, reliability
- [ ] Check locations: probe coverage
- [ ] Review documentation: quality, examples
- [ ] Test reliability: false positive rate
- [ ] Evaluate support: response time, quality
- [ ] Consider lock-in: data export, migration
- [ ] Read reviews: G2, Capterra, HN discussions
- [ ] Trial period: hands-on testing

## Updates

This criteria list should be updated when:
- New monitoring types emerge
- Industry standards change
- New competitors enter market
- User needs evolve
- Technology advances

**Last Updated**: 2026-03-21
