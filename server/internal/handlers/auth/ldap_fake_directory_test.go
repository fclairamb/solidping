package auth

import (
	"fmt"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	ber "github.com/go-asn1-ber/asn1-ber"
	"github.com/go-ldap/ldap/v3"
	"github.com/jimlambrt/gldap"
	"github.com/stretchr/testify/require"
)

// fakeLDAPDirectory is a minimal in-process LDAP test double built directly
// on github.com/jimlambrt/gldap — NOT gldap's own testdirectory sub-package
// and NOT a mock of LDAPService's own bind method — so the tests in
// ldap_service_test.go exercise a real LDAP wire-protocol round trip end to
// end, including real filter compilation via ldap.CompileFilter.
//
// Why not gldap/testdirectory: its built-in search route matches filters
// against entry DNs with a crude, non-attribute-aware substring check (see
// its unexported match() helper), not real per-attribute boolean filter
// evaluation. A meaningful LDAP-injection test needs a search backend that
// actually implements AND/OR/equality semantics — otherwise there's nothing
// for an injected filter to bypass, and nothing for escaping to prevent.
// This fake evaluates filters for real (evalLDAPFilter below), against
// explicit per-entry attribute maps, using the same compiled filter tree
// go-ldap's own client produces.
type fakeLDAPDirectory struct {
	addr string

	mu                       sync.Mutex
	entries                  []*fakeLDAPEntry
	serviceBindDN            string
	serviceBindPassword      string
	allowUnauthenticatedBind bool // simulates RFC 4513's "unauth bind succeeds" server behavior
}

// fakeLDAPEntry is one directory entry: a bind-able DN/password pair plus
// its searchable attributes (lower-cased attribute names).
type fakeLDAPEntry struct {
	dn       string
	password string
	attrs    map[string][]string
}

func newFakeLDAPEntry(dn, password string, attrs map[string][]string) *fakeLDAPEntry {
	lower := make(map[string][]string, len(attrs))
	for k, v := range attrs {
		lower[strings.ToLower(k)] = v
	}

	return &fakeLDAPEntry{dn: dn, password: password, attrs: lower}
}

// startFakeLDAPDirectory starts the fake directory on a free localhost port
// and registers its shutdown with t.Cleanup.
func startFakeLDAPDirectory(t *testing.T) *fakeLDAPDirectory {
	t.Helper()

	d := &fakeLDAPDirectory{}

	srv, err := gldap.NewServer()
	require.NoError(t, err)

	mux, err := gldap.NewMux()
	require.NoError(t, err)
	require.NoError(t, mux.Bind(d.handleBind))
	require.NoError(t, mux.Search(d.handleSearch))
	require.NoError(t, srv.Router(mux))

	d.addr = fmt.Sprintf("127.0.0.1:%d", freeTCPPort(t))

	go func() {
		_ = srv.Run(d.addr)
	}()
	t.Cleanup(func() { _ = srv.Stop() })

	for !srv.Ready() {
		time.Sleep(5 * time.Millisecond)
	}

	return d
}

func freeTCPPort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	tcpAddr, ok := listener.Addr().(*net.TCPAddr)
	require.True(t, ok)

	return tcpAddr.Port
}

func (d *fakeLDAPDirectory) serverURL() string {
	return "ldap://" + d.addr
}

func (d *fakeLDAPDirectory) setServiceAccount(dn, password string) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.serviceBindDN = dn
	d.serviceBindPassword = password
}

func (d *fakeLDAPDirectory) setEntries(entries ...*fakeLDAPEntry) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.entries = entries
}

// setAllowUnauthenticatedBind simulates a (dangerously configured, but
// real-world-common) directory that honors RFC 4513 unauthenticated binds:
// a bind with a known DN and an EMPTY password succeeds. Used to prove our
// application-level empty-password guard catches the case even when the
// directory itself would not.
func (d *fakeLDAPDirectory) setAllowUnauthenticatedBind(allow bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.allowUnauthenticatedBind = allow
}

func (d *fakeLDAPDirectory) handleBind(w *gldap.ResponseWriter, r *gldap.Request) {
	resp := r.NewBindResponse(gldap.WithResponseCode(gldap.ResultInvalidCredentials))
	defer func() { _ = w.Write(resp) }()

	msg, err := r.GetSimpleBindMessage()
	if err != nil || msg.AuthChoice != gldap.SimpleAuthChoice {
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	expectedPassword, known := d.lookupBindCredential(msg.UserName)
	if !known {
		return
	}

	submitted := string(msg.Password)

	if submitted == "" {
		if d.allowUnauthenticatedBind {
			resp.SetResultCode(gldap.ResultSuccess)
		}

		return
	}

	if submitted == expectedPassword {
		resp.SetResultCode(gldap.ResultSuccess)
	}
}

// lookupBindCredential resolves the expected password for a DN attempting
// to bind, covering both the configured service account and any directory
// entry. Caller must hold d.mu.
func (d *fakeLDAPDirectory) lookupBindCredential(dn string) (string, bool) {
	if d.serviceBindDN != "" && dn == d.serviceBindDN {
		return d.serviceBindPassword, true
	}

	for _, e := range d.entries {
		if e.dn == dn {
			return e.password, true
		}
	}

	return "", false
}

func (d *fakeLDAPDirectory) handleSearch(w *gldap.ResponseWriter, r *gldap.Request) {
	res := r.NewSearchDoneResponse(gldap.WithResponseCode(gldap.ResultOperationsError))
	defer func() { _ = w.Write(res) }()

	msg, err := r.GetSearchMessage()
	if err != nil {
		return
	}

	pkt, err := ldap.CompileFilter(msg.Filter)
	if err != nil {
		// A filter the client itself couldn't compile into valid BER never
		// reaches a real directory either — treat it the same way here.
		return
	}

	d.mu.Lock()
	entries := append([]*fakeLDAPEntry(nil), d.entries...)
	d.mu.Unlock()

	for _, e := range entries {
		if !evalLDAPFilter(pkt, e.attrs) {
			continue
		}

		entry := r.NewSearchResponseEntry(e.dn)
		for name, values := range e.attrs {
			entry.AddAttribute(name, values)
		}

		if writeErr := w.Write(entry); writeErr != nil {
			return
		}
	}

	res.SetResultCode(gldap.ResultSuccess)
}

// evalLDAPFilter evaluates a compiled RFC 4515 filter (as produced by
// ldap.CompileFilter) against an entry's attribute map (lower-cased
// attribute names, case-insensitive value comparison — like real directory
// string attributes). Supports exactly what the tests need: AND/OR/NOT,
// equality matching, and presence. This is a test double, not a
// general-purpose LDAP filter engine (no substrings/ranges/extensible
// match).
func evalLDAPFilter(pkt *ber.Packet, attrs map[string][]string) bool {
	switch pkt.Tag {
	case ldap.FilterAnd:
		for _, child := range pkt.Children {
			if !evalLDAPFilter(child, attrs) {
				return false
			}
		}

		return true
	case ldap.FilterOr:
		for _, child := range pkt.Children {
			if evalLDAPFilter(child, attrs) {
				return true
			}
		}

		return false
	case ldap.FilterNot:
		return !evalLDAPFilter(pkt.Children[0], attrs)
	case ldap.FilterEqualityMatch:
		name := strings.ToLower(ber.DecodeString(pkt.Children[0].Data.Bytes()))
		value := ber.DecodeString(pkt.Children[1].Data.Bytes())

		for _, v := range attrs[name] {
			if strings.EqualFold(v, value) {
				return true
			}
		}

		return false
	case ldap.FilterPresent:
		name := strings.ToLower(ber.DecodeString(pkt.Data.Bytes()))

		return len(attrs[name]) > 0
	default:
		return false
	}
}
