---
sidebar_position: 3
title: Notifications
---

# Notifications Configuration

SolidPing supports multiple notification channels to alert you when incidents occur.

## Supported Channels

| Channel | Status | Configuration |
|---------|--------|---------------|
| Slack | Available | OAuth integration |
| Discord | Available | Webhook |
| Email | Available | SMTP |
| Webhooks | Available | HTTP POST |
| Google Chat | Available | Webhook |
| Mattermost | Available | Webhook |
| ntfy | Available | HTTP push |
| Opsgenie | Available | API integration |
| Pushover | Available | API integration |

## Email (SMTP)

Configure email notifications via SMTP.

### Environment Variables

```bash
SP_EMAIL_ENABLED=true
SP_EMAIL_HOST=smtp.example.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=noreply@example.com
SP_EMAIL_PASSWORD=your-smtp-password
SP_EMAIL_FROM=noreply@example.com
SP_EMAIL_FROMNAME=SolidPing
SP_EMAIL_AUTHTYPE=login
SP_EMAIL_PROTOCOL=starttls
SP_EMAIL_INSECURESKIPVERIFY=false
```

### Configuration File

```yaml
email:
  enabled: true
  host: smtp.example.com
  port: 587
  username: noreply@example.com
  password: your-smtp-password
  from: noreply@example.com
  from_name: SolidPing
  auth_type: login
  protocol: starttls
  insecure_skip_verify: false
```

### Auth Types

| Type | Description |
|------|-------------|
| `plain` | PLAIN authentication |
| `login` | LOGIN authentication (default) |
| `cram-md5` | CRAM-MD5 authentication |

### Protocol Options

| Protocol | Description |
|----------|-------------|
| `none` | No encryption |
| `starttls` | STARTTLS (default) |
| `ssl` | SSL/TLS |

### Provider Examples

#### Gmail

```bash
SP_EMAIL_HOST=smtp.gmail.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=your-email@gmail.com
SP_EMAIL_PASSWORD=your-app-password  # Use App Password, not your account password
SP_EMAIL_AUTHTYPE=login
```

:::note Gmail App Password
Gmail requires an App Password when 2FA is enabled. Generate one at https://myaccount.google.com/apppasswords
:::

#### SendGrid

```bash
SP_EMAIL_HOST=smtp.sendgrid.net
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=apikey
SP_EMAIL_PASSWORD=your-sendgrid-api-key
SP_EMAIL_AUTHTYPE=login
```

#### Amazon SES

```bash
SP_EMAIL_HOST=email-smtp.us-east-1.amazonaws.com
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=your-ses-smtp-username
SP_EMAIL_PASSWORD=your-ses-smtp-password
SP_EMAIL_AUTHTYPE=login
```

#### Mailgun

```bash
SP_EMAIL_HOST=smtp.mailgun.org
SP_EMAIL_PORT=587
SP_EMAIL_USERNAME=postmaster@your-domain.mailgun.org
SP_EMAIL_PASSWORD=your-mailgun-smtp-password
SP_EMAIL_AUTHTYPE=login
```

## Slack

Slack integration uses OAuth for secure access.

### Environment Variables

```bash
SP_SLACK_APP_ID=A0XXXXXXXXX
SP_SLACK_CLIENT_ID=1234567890.1234567890123
SP_SLACK_CLIENT_SECRET=your-client-secret
SP_SLACK_SIGNING_SECRET=your-signing-secret
```

### Configuration File

```yaml
slack:
  app_id: A0XXXXXXXXX
  client_id: "1234567890.1234567890123"
  client_secret: your-client-secret
  signing_secret: your-signing-secret
```

### Setting Up a Slack App

1. Go to https://api.slack.com/apps
2. Click "Create New App" → "From scratch"
3. Name it "SolidPing" and select your workspace
4. Go to "OAuth & Permissions" and add scopes:
   - `chat:write`
   - `chat:write.public`
   - `channels:read`
5. Go to "Basic Information" to get your credentials
6. Install the app to your workspace

### Notification Format

Slack notifications include:
- Service name and URL
- Status change (Up → Down, Down → Up)
- Error details (for failures)
- Direct link to the check in SolidPing

## Discord

Discord notifications use webhooks.

### Setting Up Discord Webhooks

1. In your Discord server, go to Server Settings → Integrations
2. Click "Webhooks" → "New Webhook"
3. Name it "SolidPing" and select the channel
4. Copy the Webhook URL
5. Add the webhook URL in SolidPing's integration settings

### Webhook URL Format

```
https://discord.com/api/webhooks/{webhook.id}/{webhook.token}
```

## Google Chat

Google Chat notifications use incoming webhooks.

### Setting Up Google Chat Webhooks

1. In Google Chat, open the space where you want notifications
2. Click the space name → "Apps & integrations" → "Manage webhooks"
3. Click "Add webhook", name it "SolidPing", and copy the URL
4. Add the webhook URL in SolidPing's integration settings

## Mattermost

Mattermost notifications use incoming webhooks, similar to Slack.

### Setting Up Mattermost Webhooks

1. In Mattermost, go to Main Menu → Integrations → Incoming Webhooks
2. Click "Add Incoming Webhook"
3. Select the channel and fill in the details
4. Copy the webhook URL
5. Add the webhook URL in SolidPing's integration settings

## ntfy

[ntfy](https://ntfy.sh) is a simple HTTP-based pub/sub notification service. SolidPing can push notifications to any ntfy topic.

### Configuration

Add a ntfy connection in SolidPing with:
- **Server URL**: `https://ntfy.sh` (or your self-hosted instance)
- **Topic**: Your topic name (e.g., `solidping-alerts`)

### Example

```
# Subscribe to notifications
ntfy subscribe solidping-alerts
```

## Opsgenie

Opsgenie integration for incident management and on-call alerting.

### Configuration

Add an Opsgenie connection in SolidPing with:
- **API Key**: Your Opsgenie API integration key

### Setting Up Opsgenie

1. In Opsgenie, go to Settings → Integrations
2. Add a new "API" integration
3. Copy the API key
4. Add it in SolidPing's integration settings

## Pushover

Pushover delivers real-time notifications to your mobile devices and desktop.

### Configuration

Add a Pushover connection in SolidPing with:
- **User Key**: Your Pushover user key
- **API Token**: Your Pushover application API token

### Setting Up Pushover

1. Create an account at https://pushover.net
2. Create a new Application/API Token for SolidPing
3. Add your user key and API token in SolidPing's integration settings

## Webhooks

Generic webhooks send HTTP POST requests to any URL.

### Payload Format

```json
{
  "event": "incident.created",
  "timestamp": "2024-01-15T10:30:00Z",
  "check": {
    "uid": "550e8400-e29b-41d4-a716-446655440000",
    "name": "API Health Check",
    "url": "https://api.example.com/health"
  },
  "incident": {
    "uid": "550e8400-e29b-41d4-a716-446655440001",
    "status": "active",
    "started_at": "2024-01-15T10:30:00Z"
  },
  "result": {
    "status": "down",
    "duration_ms": 5000,
    "error": "Connection timeout"
  }
}
```

### Event Types

| Event | Description |
|-------|-------------|
| `incident.created` | New incident (check started failing) |
| `incident.escalated` | Incident reached escalation threshold |
| `incident.resolved` | Incident resolved (check recovered) |

### Webhook Configuration

In the SolidPing dashboard:
1. Go to Settings → Integrations
2. Add a new Webhook connection
3. Configure:
   - **URL**: Your webhook endpoint
   - **Method**: POST (default)
   - **Headers**: Custom headers (e.g., Authorization)
   - **Secret**: HMAC secret for signature verification

### Signature Verification

Webhooks include an `X-Signature-256` header containing an HMAC-SHA256 signature:

```python
import hmac
import hashlib

def verify_webhook(payload, signature, secret):
    expected = hmac.new(
        secret.encode(),
        payload.encode(),
        hashlib.sha256
    ).hexdigest()
    return hmac.compare_digest(f"sha256={expected}", signature)
```

## Incident Thresholds

Configure when notifications are sent per check:

| Setting | Default | Description |
|---------|---------|-------------|
| `incident_threshold` | 1 | Failures before creating incident |
| `escalation_threshold` | 3 | Failures before escalation |
| `recovery_threshold` | 1 | Successes before resolving |

### Example

```
incident_threshold: 2    # Notify after 2 consecutive failures
escalation_threshold: 5  # Escalate after 5 consecutive failures
recovery_threshold: 2    # Resolve after 2 consecutive successes
```

:::tip On-call rotations
For rotation schedules and multi-step paging (page the next person if nobody acknowledges), see [On-Call & Escalation](/features/on-call).
:::

## Testing Notifications

Test your notification setup:

1. Create a check for a known-failing endpoint
2. Wait for the incident to trigger
3. Verify notifications are received
4. Fix the endpoint and verify resolution notification

Or use the API:

```bash
# Send test notification (requires auth)
curl -X POST http://localhost:4000/api/v1/orgs/default/connections/{uid}/test \
  -H "Authorization: Bearer $TOKEN"
```
