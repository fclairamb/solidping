# Gatus — Alerting In-Depth

## Alert Configuration

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

## Provider-Specific Configuration

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
