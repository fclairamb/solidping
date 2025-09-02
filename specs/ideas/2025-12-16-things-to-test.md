By default we could test these things:

HTTP:
- https://www.cloudflare.com/cdn-cgi/trace
- https://www.google.com/
- https://httpbin.org/delay/3
- https://httpbin.org/headers
- https://github.com/
- https://one.one.one.one

These options should be tested:
- HTTP/2 & HTTP/3
- TLS
- WebSockets

DNS:
- google.com
- github.com

TCP:
- echo.feste.us:7 (port 7) - Echo protocol
- time.nist.gov:37 (port 37) - Time protocol
- whois.iana.org:43 (port 43) - WHOIS service
- irc.libera.chat:6667 (port 6667) - IRC server

UDP:
- 1.1.1.1:53 (port 53) - DNS server
- 8.8.8.8:53 (port 53) - DNS server
- pool.ntp.org:123

ICMP:
- 8.8.8.8
- 1.1.1.1
