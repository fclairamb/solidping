The `state_entries` table shall have the following conventions:

For an incident that is posted on a connection channel we shall store around the incident the channel_id, message_id, thread_ts using:
- organization_uid = $organization_uid
- key = incidents/$incident_uid/slack/thread
- value = {"channel_id": $channel_id, "message_id": $message_id, "thread_ts": $thread_ts}
