# Heartbeat

Token-based authentication via the URL identifier. Used for cron job and heartbeat monitoring.

### POST /api/v1/heartbeat/:org/:identifier
Send a heartbeat ping. Auth: public (token in URL)

### GET /api/v1/heartbeat/:org/:identifier
Send a heartbeat ping (GET variant for simple HTTP clients). Auth: public (token in URL)
