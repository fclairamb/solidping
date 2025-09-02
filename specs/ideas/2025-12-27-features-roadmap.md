# Features Roadmap

## Priority 1: Must-Have (Core Product Value)

### Incidents + Email Alerts (Combined)
**Why P1**: Monitoring without notifications is nearly useless, but alerts without incident management leads to alert fatigue. Building both together provides the right foundation and matches how modern monitoring tools (like BetterStack) work.

**Architecture**: Incidents are the core abstraction. Alerts are triggered by incident state changes, not individual check failures.

**Scope**:

**Incident Management**:
- Auto-create incident on first check failure
- Auto-resolve incident when check recovers
- Incident state machine: `open` → `ongoing` → `resolved`
- Incident timeline showing all related check results
- Manual incident acknowledgment/notes (optional for v1)

**Email Alerts**:
- Configure email recipients per check or organization-wide
- Alerts triggered by incident state changes:
  - Incident created (first failure) - immediate notification
  - Incident ongoing (optional, configurable threshold)
  - Incident resolved (recovery) - notification
- Alert templates with incident context (affected check, duration, timeline)
- SMTP configuration at organization level

**Benefits**:
- No rate limiting hacks needed - incidents naturally group failures
- Better UX from day 1 - users get "service down" not "50 ping failures"
- Easier to extend to other channels (Slack, PagerDuty) later
- Matches user mental model of production incidents

**Dependencies**: None

---

## Priority 2: High Value (Market Expansion)

### Database Health Checks
**Why P2**: Significantly expands addressable use cases. Many users need to monitor databases, not just HTTP endpoints.

**Scope**:
- **PostgreSQL**: Connection pooling, query execution, replication lag
- **MySQL**: Connection testing, query performance
- **MongoDB**: Cluster health, replica set status

**Dependencies**: May require worker-side database client libraries

---

## Priority 3: Nice-to-Have (Differentiation & Integrations)

### Slack Alerts (with threads)
**Why P3**: Popular but not essential. Email covers basic notification needs. Adds team collaboration value.

**Scope**:
- Channel configuration per check or organization
- Thread follow-ups for incident state changes
- Rich formatting with incident timeline and graphs
- Uses incident infrastructure for smart grouping

**Dependencies**: Incidents + Email Alerts (P1) - leverages existing incident model

### OpenTelemetry Integration
**Why P3**: Advanced integration for power users. Complements rather than replaces core functionality.

**Scope**:
- Export check results in OTLP format
- Configurable sampling and batching
- Support for traces and logs
- Integration with observability platforms (Grafana, Datadog, etc.)

**Dependencies**: None (should be optional/pluggable)

---

## Recommended Implementation Order

1. **Incidents + Email Alerts** - Core monitoring value. Implementing together prevents technical debt and provides proper foundation for all future alerting channels. Unblocks production use.
2. **Database Checks** - Expands market reach. Can be developed immediately after P1 is stable.
3. **Slack Alerts** - Leverages incident infrastructure built in P1. Minimal incremental work since incident model handles the hard parts.
4. **OpenTelemetry** - Final polish for enterprise/advanced users

## Cross-Cutting Concerns
- **Incident model is the foundation**: All notification channels (email, Slack, webhooks, etc.) subscribe to incident state changes
- **Notification abstraction**: Build a pluggable notification system in P1 that makes adding new channels (P3) trivial
- Database checks require careful worker architecture planning (client libraries, connection pooling)
- All features need consistent configuration UI/API patterns
- Consider webhook support early - it's often requested and fits naturally into the incident notification model
