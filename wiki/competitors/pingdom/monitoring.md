# Pingdom — Monitoring

## Check Types In-Depth

### HTTP/HTTPS Monitoring

**How it works**: Performs HTTP GET or POST requests to specified URL, checking for:
- Successful HTTP status code (200-299 by default)
- Optional: Presence of specific string in response
- Optional: Absence of specific string in response
- SSL certificate validity (for HTTPS)

**Configuration**:
- URL path and query parameters
- HTTP method (GET, POST)
- Custom headers
- POST data
- Basic/Digest authentication
- SSL certificate verification
- String matching (shouldcontain/shouldnotcontain)

**Limitations**:
- Only fetches HTML code and headers
- Does not load dynamic content (JavaScript, images, CSS)
- 30-second timeout
- No JavaScript execution (use Transaction monitoring for that)

**Use cases**:
- Website uptime monitoring
- API health checks
- Server availability
- Content verification

### Transaction Monitoring

**How it works**: Uses real Chrome browser to simulate user interactions:
- Executes JavaScript
- Loads all page resources
- Simulates clicks, form fills, navigation
- Verifies successful completion of multi-step flows

**Configuration**:
- Recorded browser scenarios
- Custom scripts
- Expected outcomes
- Timeout thresholds
- Alert conditions

**Use cases**:
- Shopping cart checkout flows
- User registration processes
- Login workflows
- Search functionality
- Multi-step form completion
- SPA (Single Page Application) testing

**Limitations**:
- Requires Pro plan (not included in basic tiers)
- More expensive than uptime checks
- Longer execution time
- Complex setup for advanced scenarios

### Ping Monitoring

**How it works**: Sends 5 ICMP packets to target host:
- Each packet has 5-second timeout
- Considers host down if 3 of 5 packets fail
- Measures packet loss and latency

**Use cases**:
- Server connectivity
- Network device monitoring
- Basic host availability

**Limitations**:
- Not suitable for websites (server may respond while website is down)
- ICMP may be blocked by firewalls
- No application-layer verification

### TCP/UDP Port Monitoring

**How it works**:
- Connects to specified port on target host
- Optionally sends string and expects response
- Verifies port is accepting connections

**TCP Configuration**:
- Hostname/IP
- Port number
- String to send (optional)
- Expected response string (optional)

**Use cases**:
- Database monitoring (PostgreSQL, MySQL, MongoDB)
- Custom service monitoring
- Application server health
- FTP server monitoring
- Any TCP-based service

**UDP Configuration**:
- Similar to TCP but using UDP protocol
- Less reliable (connectionless)

### DNS Monitoring

**How it works**:
- Queries specified DNS server
- Verifies expected IP address is returned
- Checks DNS resolution time

**Configuration**:
- DNS server to query
- Domain to resolve
- Expected IP address

**Use cases**:
- DNS server functionality
- DNS record verification
- Authoritative nameserver monitoring
- DNS propagation checking

**Limitations**:
- One check per DNS server
- Does not check recursive DNS
- Limited to simple A record queries

### Email Server Monitoring (SMTP/POP3/IMAP)

**How it works**: Connects to mail server and verifies response code

**SMTP**:
- Connects to port 25 (or custom)
- Expects 220 response code (customizable)
- Does not send actual emails
- Can use TLS encryption

**POP3/POP3S**:
- Connects to POP3 port (110 or 995)
- Verifies server responds correctly
- Supports SSL/TLS

**IMAP/IMAPS**:
- Connects to IMAP port (143 or 993)
- Verifies server availability
- Supports SSL/TLS

**Use cases**:
- Mail server uptime
- Email infrastructure monitoring
- MX server availability

## Monitoring Locations

### Global Probe Network

Pingdom operates **100+ probe servers** distributed globally across five main regions:

**Regions**:
1. **North America** - USA, Canada
2. **Europe** - UK, Germany, France, Netherlands, etc.
3. **Asia Pacific** - Singapore, Japan, Australia, etc.
4. **Latin America** - Brazil, etc.
5. **World** - All regions combined

**Region Selection**:
- Choose specific region for checks
- Default: North America and Europe
- Up to 10 probe servers per check from selected region
- Different probes test from varied locations within region

**Benefits**:
- Detect location-specific issues
- Monitor CDN performance
- Identify regional outages
- Verify geographic redundancy

**Access**:
- View probe servers: Synthetics → Probe Servers
- URL: https://my.pingdom.com/app/probes
- API endpoint: GET /probes
