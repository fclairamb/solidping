package checksip

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	maxReadSize = 8 * 1024 // 8KB is plenty for a SIP OPTIONS/REGISTER response.

	// digestNonceCount is the fixed nc value for the single-shot REGISTER flow.
	digestNonceCount = "00000001"

	microsecondsPerMilli = 1000.0

	// qopAuth is the RFC 2617 "auth" quality-of-protection directive.
	qopAuth = "auth"

	// errNotValidSIP is the diagnostic for an unparseable (non-SIP) reply.
	errNotValidSIP = "reply is not a valid SIP response"
)

// SIPChecker implements the Checker interface for SIP checks.
type SIPChecker struct{}

// Type returns the check type identifier.
func (c *SIPChecker) Type() checkerdef.CheckType {
	return checkerdef.CheckTypeSIP
}

// Validate checks if the configuration is valid. It performs no network operations.
func (c *SIPChecker) Validate(spec *checkerdef.CheckSpec) error {
	cfg := &SIPConfig{}
	if err := cfg.FromMap(spec.Config); err != nil {
		return err
	}

	if err := cfg.Validate(); err != nil {
		return err
	}

	if spec.Slug == "" {
		spec.Slug = "sip-" + strings.ReplaceAll(cfg.Host, ".", "-")
	}

	return nil
}

// applyDefaults fills in transport/port/timeout/mode/domain defaults on a copy
// of the config so Execute always works with fully-resolved values.
func applyDefaults(cfg *SIPConfig) {
	if cfg.Transport == "" {
		cfg.Transport = transportUDP
	}

	if cfg.Mode == "" {
		cfg.Mode = modeOptions
	}

	if cfg.Port == 0 {
		if cfg.Transport == transportTLS {
			cfg.Port = defaultPortTLS
		} else {
			cfg.Port = defaultPortPlain
		}
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = defaultTimeout
	}

	if cfg.Domain == "" {
		cfg.Domain = cfg.Host
	}
}

// Execute performs the SIP check and returns the result.
func (c *SIPChecker) Execute(ctx context.Context, config checkerdef.Config) (*checkerdef.Result, error) {
	cfg, err := checkerdef.AssertConfig[*SIPConfig](config)
	if err != nil {
		return nil, err
	}

	// Work on a copy so defaults don't mutate the caller's config.
	local := *cfg
	applyDefaults(&local)

	start := time.Now()

	// Resolve hostname (StatusError on failure, like checkudp/checktcp).
	addrs, err := checkerdef.LookupIPAddr(ctx, local.Host)
	if err != nil {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: fmt.Sprintf("failed to resolve hostname: %v", err),
			},
		}, nil
	}

	if len(addrs) == 0 {
		return &checkerdef.Result{
			Status:   checkerdef.StatusError,
			Duration: time.Since(start),
			Output: map[string]any{
				checkerdef.OutputKeyError: "no IP addresses found for host",
			},
		}, nil
	}

	var result checkerdef.Result
	if local.Mode == modeRegister {
		result = c.executeRegister(ctx, &local)
	} else {
		result = c.executeOptions(ctx, &local)
	}

	result.Duration = time.Since(start)

	if result.Output == nil {
		result.Output = make(map[string]any)
	}

	result.Output[checkerdef.OutputKeyHost] = local.Host
	result.Output[checkerdef.OutputKeyPort] = local.Port
	result.Output["transport"] = local.Transport
	result.Output["mode"] = local.Mode

	if result.Metrics == nil {
		result.Metrics = make(map[string]any)
	}

	result.Metrics["response_time_ms"] = float64(result.Duration.Microseconds()) / microsecondsPerMilli

	if code, ok := result.Output["sip_status"].(int); ok {
		result.Metrics["sip_status_code"] = code
	}

	return &result, nil
}

// executeOptions sends a single OPTIONS request and classifies the reply.
func (c *SIPChecker) executeOptions(ctx context.Context, cfg *SIPConfig) checkerdef.Result {
	ids := newRequestIDs()
	req := buildRequest("OPTIONS", cfg, ids, "")

	raw, err := roundTrip(ctx, cfg, req)
	if err != nil {
		return classifyTransportError(err)
	}

	code, reason, ok := parseStatusLine(raw)
	if !ok {
		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{checkerdef.OutputKeyError: errNotValidSIP},
		}
	}

	output := sipOutput(code, reason, raw)

	// Any well-formed SIP response means the server is alive (the OPTIONS
	// inversion). expect_status, if set, narrows this.
	if cfg.ExpectStatus != "" && !statusMatches(cfg.ExpectStatus, code) {
		output[checkerdef.OutputKeyError] = fmt.Sprintf("status %d not in expect_status %q", code, cfg.ExpectStatus)

		return checkerdef.Result{Status: checkerdef.StatusDown, Output: output}
	}

	return checkerdef.Result{Status: checkerdef.StatusUp, Output: output}
}

// executeRegister performs the two-shot digest-auth REGISTER handshake.
func (c *SIPChecker) executeRegister(ctx context.Context, cfg *SIPConfig) checkerdef.Result {
	ids := newRequestIDs()

	// 1. Unauthenticated REGISTER.
	req := buildRequest("REGISTER", cfg, ids, "")

	raw, err := roundTrip(ctx, cfg, req)
	if err != nil {
		return classifyTransportError(err)
	}

	code, reason, ok := parseStatusLine(raw)
	if !ok {
		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{checkerdef.OutputKeyError: errNotValidSIP},
		}
	}

	// Some servers register open relays without a challenge — accept 200 OK.
	if code == 200 {
		return checkerdef.Result{Status: checkerdef.StatusUp, Output: sipOutput(code, reason, raw)}
	}

	// 2. Expect a 401/407 challenge.
	if code != 401 && code != 407 {
		out := sipOutput(code, reason, raw)
		out[checkerdef.OutputKeyError] = fmt.Sprintf("unexpected status %d before authentication", code)

		return checkerdef.Result{Status: checkerdef.StatusDown, Output: out}
	}

	challengeHeader := extractChallengeHeader(raw, code)
	if challengeHeader == "" {
		out := sipOutput(code, reason, raw)
		out[checkerdef.OutputKeyError] = "missing authentication challenge header"

		return checkerdef.Result{Status: checkerdef.StatusDown, Output: out}
	}

	challenge := parseAuthChallenge(challengeHeader)
	// Use the per-transaction random cnonce when the server didn't supply one.
	if challenge["cnonce"] == "" {
		challenge["cnonce"] = ids.cnonce
	}

	// 3 + 4. Compute digest, resend with incremented CSeq + new Via branch.
	authUser := cfg.AuthUsername
	if authUser == "" {
		authUser = cfg.Username
	}

	digestURI := "sip:" + cfg.Domain
	authValue := buildDigestAuthorization(challenge, "REGISTER", digestURI, authUser, cfg.Password)

	ids2 := ids.next()
	authReq := buildRequest("REGISTER", cfg, ids2, authValue)

	raw2, err := roundTrip(ctx, cfg, authReq)
	if err != nil {
		return classifyTransportError(err)
	}

	code2, reason2, ok := parseStatusLine(raw2)
	if !ok {
		return checkerdef.Result{
			Status: checkerdef.StatusDown,
			Output: map[string]any{checkerdef.OutputKeyError: errNotValidSIP},
		}
	}

	out := sipOutput(code2, reason2, raw2)

	// 5. Only a final 200 OK means registered.
	if code2 == 200 {
		return checkerdef.Result{Status: checkerdef.StatusUp, Output: out}
	}

	out[checkerdef.OutputKeyError] = fmt.Sprintf("registration failed with status %d after authentication", code2)

	return checkerdef.Result{Status: checkerdef.StatusDown, Output: out}
}

// sipOutput builds the standard result body for a SIP response.
func sipOutput(code int, reason, raw string) map[string]any {
	out := map[string]any{
		"sip_status": code,
		"sip_reason": reason,
	}

	if server := headerValue(raw, "Server"); server != "" {
		out["server"] = server
	} else if ua := headerValue(raw, "User-Agent"); ua != "" {
		out["server"] = ua
	}

	return out
}

// classifyTransportError maps a transport error to the right Down/Timeout status.
func classifyTransportError(err error) checkerdef.Result {
	if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
		return checkerdef.Result{
			Status: checkerdef.StatusTimeout,
			Output: map[string]any{checkerdef.OutputKeyError: "no response before deadline"},
		}
	}

	return checkerdef.Result{
		Status: checkerdef.StatusDown,
		Output: map[string]any{checkerdef.OutputKeyError: err.Error()},
	}
}

func isTimeout(err error) bool {
	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}

// requestIDs carries the per-transaction identifiers shared across the REGISTER
// handshake (Call-ID and From tag stay constant; the branch and CSeq advance).
type requestIDs struct {
	callID  string
	fromTag string
	branch  string
	cseq    int
	cnonce  string
}

func newRequestIDs() requestIDs {
	return requestIDs{
		callID:  randomHex(16),
		fromTag: randomHex(8),
		branch:  "z9hG4bK" + randomHex(8),
		cseq:    1,
		cnonce:  randomHex(8),
	}
}

// next returns a copy with an incremented CSeq and a fresh Via branch, reusing
// the same Call-ID / From tag (per RFC 3261 for a re-sent REGISTER).
func (r requestIDs) next() requestIDs {
	r.cseq++
	r.branch = "z9hG4bK" + randomHex(8)

	return r
}

// buildRequest assembles a SIP request message with all mandatory headers.
func buildRequest(method string, cfg *SIPConfig, ids requestIDs, authHeader string) string {
	requestURI := "sip:" + cfg.Domain

	viaTransport := strings.ToUpper(cfg.Transport)
	if viaTransport == "" {
		viaTransport = "UDP"
	}

	// Local Via host is informational for a probe; we never expect inbound
	// requests, so a placeholder address is fine.
	localContact := "sip:solidping@" + cfg.Host

	user := cfg.Username
	if user == "" {
		user = "solidping"
	}

	fromURI := fmt.Sprintf("sip:%s@%s", user, cfg.Domain)
	toURI := fromURI

	var buf strings.Builder

	fmt.Fprintf(&buf, "%s %s SIP/2.0\r\n", method, requestURI)
	fmt.Fprintf(&buf, "Via: SIP/2.0/%s %s;branch=%s;rport\r\n", viaTransport, cfg.Host, ids.branch)
	buf.WriteString("Max-Forwards: 70\r\n")
	fmt.Fprintf(&buf, "From: <%s>;tag=%s\r\n", fromURI, ids.fromTag)
	fmt.Fprintf(&buf, "To: <%s>\r\n", toURI)
	fmt.Fprintf(&buf, "Call-ID: %s@%s\r\n", ids.callID, cfg.Host)
	fmt.Fprintf(&buf, "CSeq: %d %s\r\n", ids.cseq, method)
	fmt.Fprintf(&buf, "Contact: <%s>\r\n", localContact)
	buf.WriteString("User-Agent: SolidPing\r\n")

	if authHeader != "" {
		fmt.Fprintf(&buf, "Authorization: %s\r\n", authHeader)
	}

	if method == "REGISTER" {
		buf.WriteString("Expires: 60\r\n")
	}

	buf.WriteString("Content-Length: 0\r\n")
	buf.WriteString("\r\n")

	return buf.String()
}

// roundTrip dials the configured transport, sends the request and reads the
// response under the deadline.
func roundTrip(ctx context.Context, cfg *SIPConfig, request string) (string, error) {
	ctxTimeout, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	switch cfg.Transport {
	case transportTCP:
		return roundTripStream(ctxTimeout, cfg, request, false)
	case transportTLS:
		return roundTripStream(ctxTimeout, cfg, request, true)
	default:
		return roundTripUDP(ctxTimeout, cfg, request)
	}
}

// roundTripUDP does a single send + single datagram read (no T1 retransmission).
func roundTripUDP(ctx context.Context, cfg *SIPConfig, request string) (string, error) {
	target := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctx, "udp", target)
	if err != nil {
		return "", err
	}

	defer func() { _ = conn.Close() }()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(cfg.Timeout)
	}

	if err = conn.SetDeadline(deadline); err != nil {
		return "", err
	}

	if _, err = conn.Write([]byte(request)); err != nil {
		return "", err
	}

	buf := make([]byte, maxReadSize)

	n, err := conn.Read(buf)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}

	return string(buf[:n]), nil
}

// roundTripStream handles TCP and TLS: write the request, then read until we
// have a full SIP message (headers + Content-Length body) or the deadline hits.
func roundTripStream(ctx context.Context, cfg *SIPConfig, request string, useTLS bool) (string, error) {
	target := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))

	dialer := &net.Dialer{}

	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return "", err
	}

	defer func() { _ = conn.Close() }()

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(cfg.Timeout)
	}

	if useTLS {
		serverName := cfg.TLSServerName
		if serverName == "" {
			serverName = cfg.Host
		}

		tlsConn := tls.Client(conn, &tls.Config{
			ServerName:         serverName,
			InsecureSkipVerify: !cfg.TLSVerify, // Verification is opt-in via tls_verify, mirroring checktcp.
		})

		if err := tlsConn.HandshakeContext(ctx); err != nil {
			return "", err
		}

		conn = tlsConn
	}

	if err := conn.SetDeadline(deadline); err != nil {
		return "", err
	}

	if _, err := conn.Write([]byte(request)); err != nil {
		return "", err
	}

	return readStreamResponse(conn)
}

// readStreamResponse reads a SIP response from a stream connection, honoring
// the Content-Length header for the (usually empty) body.
func readStreamResponse(conn net.Conn) (string, error) {
	buf := make([]byte, 0, maxReadSize)
	chunk := make([]byte, maxReadSize)

	for len(buf) < maxReadSize {
		n, err := conn.Read(chunk)
		if n > 0 {
			buf = append(buf, chunk[:n]...)

			if msg, complete := streamMessageComplete(buf); complete {
				return msg, nil
			}
		}

		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(buf) == 0 {
					return "", io.EOF
				}

				return string(buf), nil
			}

			return "", err
		}
	}

	return string(buf), nil
}

// streamMessageComplete reports whether buf holds a full SIP message (headers
// terminated by CRLFCRLF plus any Content-Length body).
func streamMessageComplete(buf []byte) (string, bool) {
	raw := string(buf)

	idx := strings.Index(raw, "\r\n\r\n")
	if idx < 0 {
		return "", false
	}

	headers := raw[:idx]
	bodyStart := idx + 4

	contentLength := 0
	if cl := headerValue(headers, "Content-Length"); cl != "" {
		if parsed, err := strconv.Atoi(strings.TrimSpace(cl)); err == nil {
			contentLength = parsed
		}
	}

	if len(raw)-bodyStart >= contentLength {
		return raw[:bodyStart+contentLength], true
	}

	return "", false
}

// parseStatusLine extracts the numeric status code and reason from a SIP
// response. ok=false when there is no "SIP/2.0 <code>" status line.
func parseStatusLine(raw string) (int, string, bool) {
	line := raw
	if idx := strings.Index(raw, "\r\n"); idx >= 0 {
		line = raw[:idx]
	} else if idx := strings.IndexByte(raw, '\n'); idx >= 0 {
		line = raw[:idx]
	}

	line = strings.TrimSpace(line)

	const prefix = "SIP/2.0 "
	if !strings.HasPrefix(line, prefix) {
		return 0, "", false
	}

	rest := strings.TrimSpace(line[len(prefix):])

	codeStr := rest
	reason := ""

	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		codeStr = rest[:sp]
		reason = strings.TrimSpace(rest[sp+1:])
	}

	code, err := strconv.Atoi(codeStr)
	if err != nil || code < 100 || code > 699 {
		return 0, "", false
	}

	return code, reason, true
}

// headerValue returns the first value of the named header (case-insensitive),
// or "" if absent.
func headerValue(raw, name string) string {
	lowerName := strings.ToLower(name)

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")

		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}

		if strings.ToLower(strings.TrimSpace(line[:colon])) == lowerName {
			return strings.TrimSpace(line[colon+1:])
		}
	}

	return ""
}

// extractChallengeHeader returns the WWW-Authenticate (401) or
// Proxy-Authenticate (407) header value from a challenge response.
func extractChallengeHeader(raw string, code int) string {
	if code == 407 {
		return headerValue(raw, "Proxy-Authenticate")
	}

	return headerValue(raw, "WWW-Authenticate")
}

// statusMatches reports whether code is present in a comma-separated
// expect_status list such as "200" or "200,405".
func statusMatches(expect string, code int) bool {
	for _, part := range strings.Split(expect, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		if want, err := strconv.Atoi(part); err == nil && want == code {
			return true
		}
	}

	return false
}

// parseAuthChallenge parses a Digest WWW-Authenticate / Proxy-Authenticate
// header value into its parameter map (lower-cased keys, unquoted values).
func parseAuthChallenge(header string) map[string]string {
	params := make(map[string]string)

	header = strings.TrimSpace(header)
	if idx := strings.IndexByte(header, ' '); idx >= 0 &&
		strings.EqualFold(strings.TrimSpace(header[:idx]), "Digest") {
		header = header[idx+1:]
	}

	for _, raw := range splitParams(header) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}

		eqIdx := strings.IndexByte(raw, '=')
		if eqIdx < 0 {
			continue
		}

		key := strings.ToLower(strings.TrimSpace(raw[:eqIdx]))
		val := strings.TrimSpace(raw[eqIdx+1:])
		val = strings.Trim(val, "\"")
		params[key] = val
	}

	return params
}

// splitParams splits a digest parameter list on commas, ignoring commas that
// appear inside double-quoted values (e.g. qop="auth,auth-int").
func splitParams(input string) []string {
	var (
		parts   []string
		current strings.Builder
		inQuote bool
	)

	for _, char := range input {
		switch {
		case char == '"':
			inQuote = !inQuote
			current.WriteRune(char)
		case char == ',' && !inQuote:
			parts = append(parts, current.String())
			current.Reset()
		default:
			current.WriteRune(char)
		}
	}

	if current.Len() > 0 {
		parts = append(parts, current.String())
	}

	return parts
}

// buildDigestAuthorization computes an RFC 2617 MD5 digest and returns a full
// "Digest ..." Authorization header value. When the challenge advertises
// qop=auth it uses the qop variant (nc + cnonce); otherwise the legacy form.
func buildDigestAuthorization(challenge map[string]string, method, uri, username, password string) string {
	realm := challenge["realm"]
	nonce := challenge["nonce"]
	qop := challenge["qop"]
	opaque := challenge["opaque"]
	cnonce := challenge["cnonce"]

	if cnonce == "" {
		cnonce = "0a4f113b" // deterministic fallback; overridden by callers needing randomness.
	}

	ha1 := md5Hex(strings.Join([]string{username, realm, password}, ":"))
	ha2 := md5Hex(strings.Join([]string{method, uri}, ":"))

	useQOP := qopHasAuth(qop)

	var response string
	if useQOP {
		response = md5Hex(strings.Join([]string{ha1, nonce, digestNonceCount, cnonce, qopAuth, ha2}, ":"))
	} else {
		response = md5Hex(strings.Join([]string{ha1, nonce, ha2}, ":"))
	}

	var buf strings.Builder

	fmt.Fprintf(&buf, "Digest username=%q, realm=%q, nonce=%q, uri=%q, response=%q",
		username, realm, nonce, uri, response)

	if algo := challenge["algorithm"]; algo != "" {
		fmt.Fprintf(&buf, ", algorithm=%s", algo)
	}

	if useQOP {
		fmt.Fprintf(&buf, ", qop=%s, nc=%s, cnonce=%q", qopAuth, digestNonceCount, cnonce)
	}

	if opaque != "" {
		fmt.Fprintf(&buf, ", opaque=%q", opaque)
	}

	return buf.String()
}

// qopHasAuth reports whether a qop directive includes the "auth" option.
func qopHasAuth(qop string) bool {
	if qop == "" {
		return false
	}

	for _, opt := range strings.Split(qop, ",") {
		if strings.TrimSpace(opt) == qopAuth {
			return true
		}
	}

	return false
}

func md5Hex(input string) string {
	sum := md5.Sum([]byte(input))

	return hex.EncodeToString(sum[:])
}

// randomHex returns n random bytes hex-encoded (2n chars). Falls back to a
// time-seeded value if the system RNG fails (never expected).
func randomHex(n int) string {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}

	return hex.EncodeToString(buf)
}
