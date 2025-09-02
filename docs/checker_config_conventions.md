
Standard checker configuration (but also optional):
- `url`: The URL to check
- `timeout`: The timeout duration for the check.

For `http`:
(Can use `host`)
- `url`: The URL to check (http://, https://)
- `method`: The HTTP method to use (e.g., "GET", "POST").
- `headers`: The HTTP headers to send with the request.
- `auth`: The authentication configuration.
  - `auth.type`: The authentication type (e.g., "basic", "digest").
  - `auth.username`: The username for authentication.
  - `auth.password`: The password for authentication.
- `expected_status_codes`: A list of expected status codes (e.g., ["200", "201", "2XX"], "2XX" by default)
- `follow_redirects`: Whether to follow redirects (false by default)
- `protocolVersion`: The protocol version to use (e.g., "1.1", "2", "3")
- `certificate` : Certificate related checks
  - `certificate.check`: Whether to check the certificate validity (true by default)
  - `certificate.expiration_days` (int) : The number of days before expiration to trigger an alert (none by default)
- `body_expect`: A string to search for in the response body (simple string match). Check passes if string is found.
- `body_reject`: A string to search for in the response body (simple string match). Check fails if string is found.
- `body_pattern`: A regular expression pattern to search for in the response body. Check passes if pattern is found.
- `body_pattern_reject`: A regular expression pattern to search for in the response body. Check fails if pattern is found.
- `headers_pattern`: A map of header names to regular expression patterns. Check passes if all specified headers match their patterns.

For `tcp`:
- `url`: TCP host to connect to (example: tcp://example.com:80)
- `data_format`: "text", "b64", "hex"
- `data_send`: The data to send to the server.
- `data_expect`: The expected data to receive from the server.
- `secure`: Whether to use TLS/SSL for the connection.

For `udp`:
- `url`: UDP host to connect to (example: udp://example.com:53)
- `data_format`: "text", "b64", "hex"
- `data_send`: The data to send to the server.
- `data_expect`: The expected data to receive from the server.

For `ntp`:
- `url`: NTP host to connect to (example: ntp://example.com)
- `version`: The NTP version to use (e.g., 3, 4).

For `dns`:
- `server`: The server to use for resolution
- `query_type`: The DNS query type (e.g., "A", "AAAA", "MX").
- `query_name`: The DNS query name.
- `query_class`: The DNS query class (e.g., "IN", "CH", "HS").

For `smtp`:
- `url`: SMTP host to connect to (example: smtp://example.com)
- `auth`: The authentication configuration.
  - `auth.username`: The username for authentication.
  - `auth.password`: The password for authentication.
- `email`: The email configuration.
  - `email.from`: The email address to send the email from.
  - `email.to`: The email address to send the email to.
  - `email.subject`: The email subject.
  - `email.body`: The email body.
  
For `snmp`:
- `url`: SNMP host to connect to (example: snmp://example.com)
- `version`: The SNMP version to use (e.g., "1", "2c", "3").
- `community`: The SNMP community string.
- `oid`: The SNMP object identifier.

For `ftp`:
- `url`: FTP host to connect to (example: ftp(s)://example.com)
- `auth`: The authentication configuration.
  - `auth.username`: The username for authentication.
  - `auth.password`: The password for authentication.
- `secure`: Whether to use FTPS or FTP
- `directory`: The directory to check.
- `file`: The file to check.

For `ssh`:
- `url`: SSH host to connect to (example: ssh://example.com)
- `auth`: The authentication configuration.
  - `auth.username`: The username for authentication.
  - `auth.password`: The password for authentication.
  - `auth.user_private_key`: The private key for authentication.
  - `auth.remote_public_key`: The remote public key for authentication.
- `command`: The command to execute on the remote server.

For `sftp`:
- `url`: SFTP host to connect to (example: sftp://example.com)
- `auth`: The authentication configuration.
  - `auth.username`: The username for authentication.
  - `auth.password`: The password for authentication.
  - `auth.key`: The private key for authentication.
- `directory`: The directory to check.
- `file`: The file to check.

For `heartbeat` check:
- `token`: The (optional) token to confirm the check is valid.
- `timeout`: The timeout in seconds.
- `grace_period`: The grace period in seconds.
