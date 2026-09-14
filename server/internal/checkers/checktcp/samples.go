package checktcp

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const (
	samplePort       = 443             // HTTPS port for sample checks
	sampleTimeout    = 5 * time.Second // Default timeout for sample TCP checks
	sampleHostGoogle = "google.com"
	// demoHost is the only host the public live demo's TCP catalog probes:
	// our own.
	demoHost = "solidping.io"

	// SampleHTTPRequest is a minimal HTTP/1.0 HEAD request against demoHost,
	// written with `escaped` encoding because CRLF cannot be typed into a
	// <textarea> any other way.
	SampleHTTPRequest = `HEAD / HTTP/1.0\r\nHost: ` + demoHost + `\r\n\r\n`
	// SampleHTTPExpect proves a web server ANSWERED, not merely that the port
	// accepted a connection and completed a TLS handshake.
	SampleHTTPExpect = `^HTTP/1\.[01] (200|301|302)`
)

// speakingSolidpingSample is the TCP sample that actually talks: connect over
// TLS, send a HEAD request, and require a status line back.
func speakingSolidpingSample() map[string]any {
	return (&TCPConfig{
		Host:          demoHost,
		Port:          samplePort,
		Timeout:       sampleTimeout,
		TLS:           true,
		TLSVerify:     true,
		SendData:      SampleHTTPRequest,
		SendEncoding:  checkerdef.PayloadEncodingEscaped,
		ExpectPattern: SampleHTTPExpect,
	}).GetConfig()
}

// GetSampleConfigs returns sample TCP check configurations.
func (c *TCPChecker) GetSampleConfigs(opts *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	// The public live demo's catalog probes SOLIDPING-OWNED HOSTS ONLY (spec
	// 2026-09-06-02): it runs from every public region, forever, on a page
	// anyone can open, and that is not bandwidth to spend on a third party.
	if opts != nil && opts.Type == checkerdef.Demo {
		return []checkerdef.CheckSpec{
			{
				Name:   "solidping.io TLS port",
				Slug:   "demo-tcp-solidping",
				Period: time.Minute,
				Config: speakingSolidpingSample(),
			},
		}
	}

	return []checkerdef.CheckSpec{
		{
			Name:   "Google HTTPS (443)",
			Slug:   "tcp-google",
			Period: time.Minute * 5,
			Config: (&TCPConfig{
				Host:    sampleHostGoogle,
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			Name:   "Cloudflare HTTPS (443)",
			Slug:   "tcp-cloudflare",
			Period: time.Minute * 5,
			Config: (&TCPConfig{
				Host:    "cloudflare.com",
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
		{
			// Same host as the first sample, pinned to IPv6: the pair is the
			// clearest illustration that one check covers one family.
			Name:   "Google HTTPS over IPv6 (443)",
			Slug:   "tcp-google-ipv6",
			Period: time.Minute * 5,
			Config: checkerdef.SampleConfigWithIPVersion((&TCPConfig{
				Host:    sampleHostGoogle,
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(), checkerdef.IPVersionIPv6),
		},
		{
			// The one sample that proves the SERVICE works, not just that the
			// port is open: it sends a request and requires a status line back.
			Name:   "solidping.io HTTPS request (443)",
			Slug:   "tcp-solidping-https",
			Period: time.Minute * 5,
			Config: speakingSolidpingSample(),
		},
		{
			Name:   "GitHub HTTPS (443)",
			Slug:   "tcp-github",
			Period: time.Minute * 5,
			Config: (&TCPConfig{
				Host:    "github.com",
				Port:    samplePort,
				Timeout: sampleTimeout,
			}).GetConfig(),
		},
	}
}
