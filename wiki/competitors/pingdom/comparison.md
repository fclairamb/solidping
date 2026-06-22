# Pingdom — Comparison, Technical Considerations, Limitations & Patterns

## Comparison with SolidPing

### Similarities

Both platforms offer:
- HTTP/HTTPS uptime monitoring
- TCP port monitoring
- Ping monitoring
- DNS monitoring
- Email server monitoring (SMTP, POP3, IMAP)
- REST APIs for programmatic access
- Alert management
- Status tracking and reporting
- Multiple notification channels

### Pingdom Advantages

1. **Established brand** - Since 2007, well-known in industry
2. **100+ global locations** - Extensive probe network
3. **Transaction monitoring** - Real Chrome browser testing
4. **Real User Monitoring** - Actual visitor analytics
5. **Page speed monitoring** - Detailed performance analysis
6. **Native mobile apps** - iOS and Android
7. **SolarWinds backing** - Enterprise support and resources
8. **Advanced reporting** - Comprehensive performance reports
9. **Multi-region selection** - Geographic redundancy
10. **Mature ecosystem** - Many integrations and third-party tools

### SolidPing Advantages

1. **Self-hosted option** - Full data ownership and control
2. **No vendor lock-in** - Own your monitoring infrastructure
3. **Cost control** - No recurring fees for self-hosted
4. **Open source potential** - Customizable and extensible
5. **Direct database access** - PostgreSQL for custom queries
6. **Privacy-first** - No third-party data sharing
7. **No monitor limits** - Unlimited monitors on self-hosted
8. **Simpler architecture** - Easier to understand
9. **API-first design** - Built for developers
10. **Better free tier potential** - Can match or beat UptimeRobot's 50 free monitors
11. **No false positives** - Control your own probe infrastructure
12. **Heartbeat monitoring** - Built-in cron job monitoring

### Feature Gaps in SolidPing

Areas where SolidPing could consider expansion to match Pingdom:

1. **Transaction monitoring** - Browser-based scenario testing
2. **Real User Monitoring** - JavaScript snippet for actual user data
3. **Page speed monitoring** - Detailed performance analysis
4. **Multi-location probes** - Geographic redundancy (for SaaS version)
5. **Mobile applications** - iOS and Android apps
6. **Advanced reporting** - Graphical reports and dashboards
7. **Root cause analysis** - Automated issue diagnosis
8. **SSL expiry alerts** - Certificate expiration tracking
9. **Custom headers** - More flexible HTTP configuration
10. **String matching** - shouldcontain/shouldnotcontain verification
11. **Maintenance windows** - Scheduled downtime handling
12. **Probe server API** - List available monitoring locations
13. **Advanced integrations** - Native Slack, PagerDuty, etc.

## Technical Considerations

### Rate Limiting

- **Not publicly documented** - Exact limits vary by plan
- **Monitor limits apply** - Account tier determines API usage
- **Best practices**:
  - Cache responses when possible
  - Implement exponential backoff
  - Monitor response headers for rate limit warnings
  - Contact support for higher limits

### API Versioning

**Current: 3.1**
- Bearer token authentication
- Active development
- Recommended for all integrations

**Legacy: 2.1**
- Basic authentication (username/password)
- Still supported
- No new features
- Security concerns (credential exposure)

**Migration**: Straightforward (change auth method, same endpoints)

### Data Retention

- **Check results**: Varies by plan (typically 30 days to 12+ months)
- **Transaction data**: Limited retention
- **RUM data**: Real-time with limited historical storage
- **Reports**: Can export historical data

### Response Times

- **Check intervals**: 1 minute minimum (60-second checks)
- **API response time**: Typically <500ms
- **Webhook delivery**: Near real-time
- **Status page updates**: Immediate

### Security

- **HTTPS required** - All API calls must use TLS
- **Bearer token auth** - Secure API key authentication
- **Token permissions** - Read-only vs read/write
- **IP whitelisting** - Not available
- **Two-factor authentication** - Available for dashboard login
- **SSL certificate verification** - Configurable per check

### Multi-Location Checking

- **5 regions available** - NA, EU, APAC, LATAM, World
- **10 probes per check** - From selected region
- **Geo-verification** - Multiple locations must confirm outage
- **False positive reduction** - Regional consensus
- **CDN monitoring** - Verify geographic distribution

## Limitations and Gaps

1. **No free tier** - Only 30-day trial (competitors offer free plans)
2. **Expensive pricing** - $10/month for just 10 monitors
3. **Limited SMS alerts** - Quota-based, additional costs
4. **False positives** - Users report frequent false alarms
5. **1-minute minimum interval** - Competitors offer 30-second checks
6. **Transaction monitoring cost** - Expensive for advanced features
7. **No heartbeat monitoring** - Missing cron job monitoring
8. **Complex pricing** - 22 different tier options
9. **Limited free RUM** - Page view quotas restrictive
10. **No self-hosted option** - SaaS-only, vendor lock-in
11. **API documentation** - JavaScript-required docs (accessibility issue)
12. **Limited customization** - Less flexible than open-source alternatives
13. **Rate limits unclear** - Not transparently documented
14. **SolarWinds ownership** - Concerns after SolarWinds security incident
15. **Alert fatigue** - False positives lead to mistrust

## API Design Patterns

### Good Patterns to Adopt

1. **Bearer token authentication** - Modern, secure approach
2. **OpenAPI documentation** - Industry standard
3. **RESTful design** - Standard HTTP methods
4. **Bulk operations** - Pause multiple checks at once
5. **Regional filtering** - probe_filters for location selection
6. **Tag-based filtering** - Organize and filter by tags
7. **Summary endpoints** - Aggregate data (average, outage, performance)
8. **Include flags** - Optional data inclusion (includeuptime, includeanalysis)
9. **Unix timestamps** - Standard time representation
10. **Probe server API** - List available monitoring locations

### Patterns to Consider

1. **Check types** - Comprehensive coverage (HTTP, TCP, UDP, SMTP, etc.)
2. **String verification** - shouldcontain/shouldnotcontain
3. **Custom headers** - HTTP request customization
4. **Maintenance windows** - Scheduled downtime support
5. **Alert actions API** - Historical alert delivery tracking
6. **Multiple check creation** - Bulk check operations
7. **Resolution parameter** - Configurable check intervals

### Patterns to Avoid

1. **No free tier** - Competitors offer generous free tiers
2. **Complex pricing** - Too many tier options (22 variations)
3. **Unclear rate limits** - Should be transparently documented
4. **JavaScript-required docs** - Accessibility and SEO issues
5. **Legacy auth support** - Security risk maintaining Basic auth
6. **Undocumented limits** - SMS quotas, API limits should be clear
7. **Expensive transaction monitoring** - Pricing barrier to entry
