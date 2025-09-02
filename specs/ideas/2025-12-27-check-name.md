Do you think `check` is a good name for the check entity in solidping ? Shouldn't it be `monitor` or `probe` instead?

## Analysis of Naming Options

### Current: "Check"
**Pros:**
- Industry standard (Pingdom "checks", Better Uptime "checks", Healthchecks.io "checks")
- Simple, accessible terminology for non-technical users
- Works well as both noun and verb ("run a check", "check the service")
- Short and easy to type in CLI commands (`sp checks list`)
- Already established throughout the codebase

**Cons:**
- Slightly ambiguous - could mean the action or the configuration
- Less precise than technical alternatives

### Alternative: "Monitor"
**Pros:**
- Very common in the industry (Datadog, New Relic, UptimeRobot use "monitor")
- Emphasizes continuous observation nature
- Professional/enterprise feel
- Clear that it's an ongoing process

**Cons:**
- Ambiguous: is it the thing monitoring or the configuration being monitored?
- "Monitor" as a verb conflicts with noun usage ("monitor a monitor"?)
- Longer to type in CLI
- Less natural in some contexts ("create a monitor" vs "create a check")

### Alternative: "Probe"
**Pros:**
- Technically precise (Prometheus uses "probe" via blackbox_exporter)
- Unambiguous - clearly the thing doing the checking
- Common in network monitoring and diagnostic tools
- Accurate representation of the technical function

**Cons:**
- More technical/intimidating for casual users
- Less intuitive for non-technical stakeholders
- Sounds more clinical/medical
- Not as common in SaaS monitoring products

## Recommendation

**Stick with "check"** for these reasons:

1. **User-friendliness**: Most accessible to both technical and non-technical users
2. **Industry alignment**: Matches terminology used by direct competitors (Pingdom, Better Uptime, Healthchecks.io)
3. **API clarity**: `/api/v1/orgs/$org/checks` reads naturally and follows REST conventions
4. **CLI brevity**: `sp checks list` is concise and easy to remember
5. **Established investment**: Already used consistently throughout codebase, API, and documentation
6. **Natural language fit**: Works well in sentences ("run a check", "this check failed", "add a new check")

The current naming is already solid. The marginal benefits of changing to "monitor" or "probe" don't outweigh the consistency already established and the potential confusion during migration.

### If starting fresh
If this were a greenfield project with no existing code, "probe" would be worth considering for its technical precision, but "check" would still win for its balance of accessibility and industry familiarity.
