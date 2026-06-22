# UptimeRobot — Heartbeat Monitoring In-Depth

Heartbeat monitoring operates inversely to traditional monitoring: instead of UptimeRobot polling your service, your service pings UptimeRobot.

## How It Works

1. **Create heartbeat monitor** in UptimeRobot (via UI or API)
2. **Receive unique URL**: `https://heartbeat.uptimerobot.com/m[unique-identifier]`
   - Example: `https://heartbeat.uptimerobot.com/m794yyyyyyyy-xxxxxxxxxxxxxxx`
3. **Configure expected interval** (e.g., every 5 minutes)
4. **Add heartbeat call to your script**
5. **Monitor marked down** if heartbeat not received within interval + grace period

## Setup Process

**Unix/Linux (Crontab)**:
```bash
# Edit crontab
crontab -e

# Add heartbeat call (every 5 minutes)
*/5 * * * * curl https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx
```

**Using wget**:
```bash
*/5 * * * * wget -q -O /dev/null https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx
```

**Windows (Task Scheduler)**:
1. Create new task in Task Scheduler
2. Set trigger to match monitor interval (e.g., every 5 minutes)
3. Create action using PowerShell:
```powershell
Invoke-WebRequest -Uri "https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx"
```
4. Configure to run when no user is logged in

**Python Script**:
```python
import requests

# Your scheduled task
def daily_backup():
    # Perform backup
    backup_data()

    # Report success to UptimeRobot
    requests.get('https://heartbeat.uptimerobot.com/m794xxx-xxxxxxxx')

# In your cron/scheduler
daily_backup()
```

## Use Cases

- **Cron jobs** - Database backups, data exports, cleanup tasks
- **Scheduled tasks** - Windows Task Scheduler jobs
- **Background workers** - Queue processors, batch jobs
- **Serverless functions** - Scheduled Lambda/Cloud Functions
- **Intranet servers** - Internal servers with internet connectivity
- **Performance indicators** - Application health metrics
- **ETL processes** - Data pipeline monitoring

## Important Notes

- Heartbeat interval in cron **must match** UptimeRobot monitor interval
- GET or POST requests both work
- No authentication required on heartbeat URL (URL itself is secret)
- Heartbeat URL should be kept private
- Grace period provides buffer for timing variations
- Pro plan required for heartbeat monitoring
