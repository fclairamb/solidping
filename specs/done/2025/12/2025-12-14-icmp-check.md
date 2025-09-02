# ICMP Ping Check Specification

## Overview
The ICMP checker performs network reachability tests using ICMP Echo Request/Reply messages (ping). This is useful for monitoring basic network connectivity to hosts without requiring open TCP/UDP ports.

## Implementation
- Based on `golang.org/x/net/icmp` package
- Checker type: `"ping"`
- Package location: `/back/internal/checkers/checkping/`

## Configuration

### Required Fields
- `host` (string) - Target hostname or IP address to ping

### Optional Fields
- `timeout` (duration, default: 5s) - Maximum time to wait for ICMP reply
- `count` (int, default: 1) - Number of ping packets to send
- `interval` (duration, default: 1s) - Time between ping packets (when count > 1)
- `packet_size` (int, default: 56) - Size of ping packet payload in bytes (standard is 56 bytes + 8 byte ICMP header = 64 bytes total)
- `ttl` (int, default: 64) - Time-to-live for packets

### IPv4/IPv6 Support
- Auto-detect based on host resolution
- Support both IPv4 (ICMP) and IPv6 (ICMPv6)
- If host resolves to both, prefer IPv4 by default
- Future: Add `ip_version` config option to force IPv4/IPv6

## Validation Rules

### Config Validation (Validate method)
- `host` must not be empty
- `count` must be > 0 and <= 10 (prevent excessive pinging)
- `interval` must be >= 100ms and <= 60s
- `packet_size` must be >= 0 and <= 65507 bytes (max IP packet size minus headers)
- `ttl` must be >= 1 and <= 255
- `timeout` must be > 0 and <= 60s

## Execution Behavior

### Success Criteria (StatusUp)
- At least one ICMP Echo Reply received within timeout
- Reply has matching sequence number and identifier

### Failure Criteria (StatusDown)
- No replies received within timeout for any packet
- All packets lost (100% packet loss)

### Timeout (StatusTimeout)
- Context deadline exceeded before completion

### Error (StatusError)
- Unable to resolve hostname
- Insufficient permissions (ICMP requires raw sockets/elevated privileges)
- Network interface errors
- Invalid ICMP response format

## Metrics

Returned in `Result.Metrics` (for time-series aggregation):
- `rtt_ms_min` (float64) - Minimum round-trip time in milliseconds
- `rtt_ms_max` (float64) - Maximum round-trip time in milliseconds
- `rtt_ms_avg` (float64) - Average round-trip time in milliseconds
- `packet_loss_pct` (float64) - Packet loss percentage (0.0 to 100.0)
- `packets_sent` (int) - Number of packets transmitted
- `packets_received` (int) - Number of packets received

## Output

Returned in `Result.Output` (for diagnostic information):
- `host` (string) - Resolved IP address that was pinged
- `error` (string) - Error message if check failed
- `ip_version` (string) - "ipv4" or "ipv6"
- `ttl` (int) - TTL value used

## Implementation Notes

### Privileges
- ICMP requires raw socket access
- On Linux: requires `CAP_NET_RAW` capability or root
- On macOS/Windows: typically allowed for unprivileged users
- Should handle "operation not permitted" errors gracefully

### Packet Structure
- ICMP Echo Request (Type 8 for IPv4, Type 128 for IPv6)
- Unique identifier per check execution (use process ID)
- Sequential sequence numbers for multiple packets
- Include timestamp in payload for RTT calculation

### DNS Resolution
- Resolve hostname before sending ICMP packets
- Cache resolved IP during single check execution
- Return error if resolution fails

### Future Enhancements
- Support for custom ICMP payload patterns
- Fragmentation testing with large packets
- Path MTU discovery
- Record route option
- Alert on RTT degradation thresholds
