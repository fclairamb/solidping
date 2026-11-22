package checkbrowser

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/egress"
)

// webRTCPage reports, as JSON in its title, what WebRTC its own document, a
// srcdoc iframe, an iframe it creates by script, an optional cross-site
// iframe (the "frame" query parameter) and a worker can see. When it can
// build a peer connection it gathers ICE candidates against the STUN server
// in its "stun" query parameter first.
const webRTCPage = `<!doctype html><html><head><title>waiting</title></head><body>
<p data-testid="webrtc-page">webrtc</p>
<iframe data-testid="static-frame"
  srcdoc="<script>parent.postMessage({frame: 'srcdoc', type: typeof RTCPeerConnection}, '*')</script>"></iframe>
<script>
const q = new URLSearchParams(location.search);
const report = { top: typeof RTCPeerConnection, webkit: typeof webkitRTCPeerConnection,
  channel: typeof RTCDataChannel };
const pending = new Set(["srcdoc", "worker"]);
window.addEventListener("message", (e) => {
  if (e.data && e.data.frame) { report[e.data.frame] = e.data.type; pending.delete(e.data.frame); }
});

const dynamic = document.createElement("iframe");
dynamic.setAttribute("data-testid", "dynamic-frame");
document.body.appendChild(dynamic);
report.dynamic = typeof window[window.length - 1].RTCPeerConnection;

if (q.get("frame")) {
  pending.add("cross");
  const cross = document.createElement("iframe");
  cross.setAttribute("data-testid", "cross-frame");
  cross.src = q.get("frame");
  document.body.appendChild(cross);
}

const worker = new Worker(URL.createObjectURL(new Blob(
  ["postMessage(typeof self.RTCPeerConnection)"], { type: "text/javascript" })));
worker.onmessage = (e) => { report.worker = e.data; pending.delete("worker"); };

async function gather() {
  if (typeof RTCPeerConnection !== "function") { return "unavailable"; }
  const pc = new RTCPeerConnection({ iceServers: [{ urls: "stun:" + q.get("stun") }] });
  pc.createDataChannel("probe");
  const done = new Promise((resolve) => {
    pc.onicegatheringstatechange = () => { if (pc.iceGatheringState === "complete") resolve("complete"); };
    setTimeout(() => resolve("timeout"), 5000);
  });
  await pc.setLocalDescription(await pc.createOffer());
  return await done;
}

const started = Date.now();
gather().catch((e) => "error:" + e).then(async (ice) => {
  report.ice = ice;
  while (pending.size > 0 && Date.now() - started < 8000) {
    await new Promise((r) => setTimeout(r, 50));
  }
  document.title = JSON.stringify(report);
});
</script></body></html>`

// webRTCFrame reports to its parent what WebRTC it can see.
const webRTCFrame = `<!doctype html><html><body><script>
parent.postMessage({frame: "cross", type: typeof RTCPeerConnection}, "*");
</script></body></html>`

// popupOpenerPage opens /popup, with its own query string, either with
// window.open (the "open-popup" button, which then puts what window.open
// returned in the title) or through a target=_blank link ("popup-link").
const popupOpenerPage = `<!doctype html><html><head><title>opener</title></head><body>
<button data-testid="open-popup"
  onclick="document.title = 'opened:' + String(window.open('/popup' + location.search))">open</button>
<a data-testid="popup-link" target="_blank" href="/popup">link</a>
<script>document.querySelector("[data-testid=popup-link]").href = "/popup" + location.search;</script>
</body></html>`

// popupPage gathers ICE candidates against the STUN server in its "stun"
// query parameter as soon as it loads.
const popupPage = `<!doctype html><html><head><title>popup</title></head><body>
<p data-testid="popup-page">popup</p>
<script>
const q = new URLSearchParams(location.search);
if (typeof RTCPeerConnection === "function") {
  const pc = new RTCPeerConnection({ iceServers: [{ urls: "stun:" + q.get("stun") }] });
  pc.createDataChannel("probe");
  pc.createOffer().then((offer) => pc.setLocalDescription(offer));
}
</script></body></html>`

// stunListener counts the STUN messages (magic cookie 0x2112A442) received on
// a UDP port of the IPv4 loopback, which the egress guard classes as private.
func stunListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()

	var lc net.ListenConfig

	conn, err := lc.ListenPacket(t.Context(), "udp4", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	var received atomic.Int32

	go func() {
		buf := make([]byte, 2048)

		for {
			n, _, readErr := conn.ReadFrom(buf)
			if readErr != nil {
				return
			}

			if n >= 20 && binary.BigEndian.Uint32(buf[4:8]) == 0x2112A442 {
				received.Add(1)
			}
		}
	}()

	return conn.LocalAddr().String(), &received
}

// webRTCServer serves webRTCPage on / and webRTCFrame on /frame, on the IPv6
// loopback (the "public" side, through egress.WithPublicOverride).
func webRTCServer(t *testing.T) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(webRTCPage))
	})
	mux.HandleFunc("/frame", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(webRTCFrame))
	})
	mux.HandleFunc("/opener", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(popupOpenerPage))
	})
	mux.HandleFunc("/popup", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(popupPage))
	})

	var lc net.ListenConfig

	listener, err := lc.Listen(t.Context(), "tcp", "[::1]:0")
	if err != nil {
		t.Skipf("cannot listen on the IPv6 loopback: %v", err)
	}

	server := httptest.NewUnstartedServer(mux)
	server.Listener = listener
	server.Start()
	t.Cleanup(server.Close)

	return server
}

// webRTCReport is what webRTCPage puts in its title.
type webRTCReport struct {
	Top     string `json:"top"`
	Webkit  string `json:"webkit"`
	Channel string `json:"channel"`
	Srcdoc  string `json:"srcdoc"`
	Dynamic string `json:"dynamic"`
	Cross   string `json:"cross"`
	Worker  string `json:"worker"`
	ICE     string `json:"ice"`
}

// loadWebRTCPage navigates to pageURL and decodes the page's report.
func loadWebRTCPage(ctx context.Context, t *testing.T, session *Session, pageURL string) webRTCReport {
	t.Helper()

	r := require.New(t)

	_, err := session.Navigate(ctx, pageURL)
	r.NoError(err)

	title, err := pollTitleFor(ctx, session, 20*time.Second, func(title string) bool { return title != "waiting" })
	r.NoError(err)

	var report webRTCReport
	r.NoError(json.Unmarshal([]byte(title), &report), "title %q", title)

	return report
}

// pollTitleFor is pollTitle with a longer deadline: ICE gathering alone may
// take five seconds.
func pollTitleFor(
	ctx context.Context, session *Session, wait time.Duration, done func(string) bool,
) (string, error) {
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	for {
		value, err := session.Evaluate(ctx, "document.title")
		if err != nil {
			return "", err
		}

		title, _ := value.(string)
		if done(title) {
			return title, nil
		}

		select {
		case <-waitCtx.Done():
			return title, errTitleNeverSettled
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// stunSettleTime is how long the test keeps listening after ICE gathering
// ended, for a late STUN retransmission.
const stunSettleTime = time.Second

// TestBrowserWebRTCToAPrivateAddressIsRefused is spec 2026-11-20-01's
// regression: a page served from a host the guard treats as public builds an
// RTCPeerConnection whose STUN server is a UDP listener the guard classes as
// private. Under an enforcing policy the listener must receive nothing and
// no frame, worker or iframe of the page may see RTCPeerConnection. The
// positive control, with a non-enforcing guard, proves the listener sees the
// STUN binding request when nothing stops it.
//
//nolint:paralleltest // mutates the process-wide settings
func TestBrowserWebRTCToAPrivateAddressIsRefused(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	if !proxyReachableFrom(settings) {
		t.Skip("the remote Chrome cannot reach this process's loopback: the STUN listener is out of its reach")
	}

	withSettings(t, settings)
	widenChromeStart(t)

	server := webRTCServer(t)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)

	// Only the guard knows these names, so a cross-site frame really is a
	// second site (and, under site isolation, a second renderer process).
	named := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")), egress.WithLookup(
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			if strings.HasSuffix(host, ".acme.test") {
				return []net.IPAddr{{IP: net.ParseIP("::1")}}, nil
			}

			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		},
	))

	openEnforcing := func(t *testing.T, pageURL string) (context.Context, *Session) {
		t.Helper()

		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		t.Cleanup(cancel)

		ctx = egress.WithGuard(ctx, named)

		pin, err := preflightEgress(ctx, pageURL)
		require.NoError(t, err)

		session, err := openSessionPinned(ctx, ctx, pin)
		require.NoError(t, err)
		t.Cleanup(session.Close)

		require.NotNil(t, session.proxy, "an enforcing session goes through the egress proxy")
		require.NotEmpty(t, session.webRTCScript, "an enforcing session removes WebRTC")

		return ctx, session
	}

	t.Run("not enforcing: the STUN request reaches the listener", func(t *testing.T) {
		r := require.New(t)
		stunAddr, received := stunListener(t)

		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()

		ctx = egress.WithGuard(ctx, egress.New(true))

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		r.Nil(session.proxy)
		r.Empty(session.webRTCScript)

		report := loadWebRTCPage(ctx, t, session, server.URL+"/?stun="+url.QueryEscape(stunAddr))
		r.Equal("function", report.Top)
		r.Eventually(func() bool { return received.Load() > 0 }, 5*time.Second, 50*time.Millisecond,
			"the control must see the STUN binding request, or the enforcing case proves nothing")
	})

	t.Run("enforcing: no WebRTC in any frame, nothing reaches the listener", func(t *testing.T) {
		r := require.New(t)
		stunAddr, received := stunListener(t)

		pageURL := "http://www.acme.test:" + port + "/?stun=" + url.QueryEscape(stunAddr) +
			"&frame=" + url.QueryEscape("http://frame.acme.test:"+port+"/frame")
		ctx, session := openEnforcing(t, pageURL)

		report := loadWebRTCPage(ctx, t, session, pageURL)
		r.Equal(webRTCReport{
			Top: "undefined", Webkit: "undefined", Channel: "undefined",
			Srcdoc: "undefined", Dynamic: "undefined", Cross: "undefined", Worker: "undefined",
			ICE: "unavailable",
		}, report)

		time.Sleep(stunSettleTime)
		r.Zero(received.Load(), "the private STUN listener never saw a packet")
		r.NoError(session.deniedByEgress())
	})

	// The command-line policy holds on its own, for a page that would get a
	// peer connection anyway. Only a Chrome this process starts carries it:
	// a remote Chrome's flags are its operator's.
	t.Run("enforcing, script removed: the WebRTC policy alone keeps UDP in", func(t *testing.T) {
		if settings.Remote() {
			t.Skip("a remote Chrome's command line is not set by this process")
		}

		r := require.New(t)
		stunAddr, received := stunListener(t)

		pageURL := "http://www.acme.test:" + port + "/?stun=" + url.QueryEscape(stunAddr)
		ctx, session := openEnforcing(t, pageURL)

		r.NoError(session.run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
			return page.RemoveScriptToEvaluateOnNewDocument(session.webRTCScript).Do(ctx)
		})))

		report := loadWebRTCPage(ctx, t, session, pageURL)
		r.Equal("function", report.Top, "the script is really gone")

		time.Sleep(stunSettleTime)
		r.Zero(received.Load(), "the private STUN listener never saw a packet")
	})
}

// popupSettleTime is how long a popup is given to send its STUN request.
const popupSettleTime = 3 * time.Second

// TestBrowserPopupCannotReachAPrivateAddress is spec 2026-11-22-03's
// regression. A popup is a separate top-level target: the WebRTC removal
// script and the egress watch of the session's page do not reach it. The
// Chrome is started WITHOUT the WebRTC policy flags, the way a remote
// sidecar deployed before spec 2026-11-20-01 runs, so the only defences
// left are the session's: window.open returns null, and a popup opened by a
// target=_blank link is closed at once and poisons the session. The
// positive control, with a non-enforcing guard, proves the popup does send
// the STUN request when nothing stops it.
//
//nolint:paralleltest // mutates the process-wide settings
func TestBrowserPopupCannotReachAPrivateAddress(t *testing.T) {
	settings, ok := liveBrowserSettings()
	if !ok {
		t.Skip(liveBrowserSkipReason)
	}

	if !proxyReachableFrom(settings) {
		t.Skip("the remote Chrome cannot reach this process's loopback: the STUN listener is out of its reach")
	}

	withSettings(t, settings)
	widenChromeStart(t)

	omitWebRTCPolicyFlags.Store(true)
	t.Cleanup(func() { omitWebRTCPolicyFlags.Store(false) })

	server := webRTCServer(t)

	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	require.NoError(t, err)

	named := egress.New(false, egress.WithPublicOverride(net.ParseIP("::1")), egress.WithLookup(
		func(_ context.Context, host string) ([]net.IPAddr, error) {
			if strings.HasSuffix(host, ".acme.test") {
				return []net.IPAddr{{IP: net.ParseIP("::1")}}, nil
			}

			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		},
	))

	openEnforcing := func(t *testing.T, pageURL string) (context.Context, *Session) {
		t.Helper()

		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		t.Cleanup(cancel)

		ctx = egress.WithGuard(ctx, named)

		pin, err := preflightEgress(ctx, pageURL)
		require.NoError(t, err)

		session, err := openSessionPinned(ctx, ctx, pin)
		require.NoError(t, err)
		t.Cleanup(session.Close)

		return ctx, session
	}

	t.Run("not enforcing: the popup's STUN request reaches the listener", func(t *testing.T) {
		r := require.New(t)
		stunAddr, received := stunListener(t)

		ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
		defer cancel()

		ctx = egress.WithGuard(ctx, egress.New(true))

		session, err := Open(ctx)
		r.NoError(err)

		defer session.Close()

		_, err = session.Navigate(ctx, server.URL+"/opener?stun="+url.QueryEscape(stunAddr))
		r.NoError(err)
		r.NoError(session.Click(ctx, "[data-testid=popup-link]"))

		r.Eventually(func() bool { return received.Load() > 0 }, 10*time.Second, 50*time.Millisecond,
			"the control must see the popup's STUN request, or the enforcing cases prove nothing")
		r.NoError(session.deniedByEgress())
	})

	t.Run("enforcing: window.open returns null", func(t *testing.T) {
		r := require.New(t)
		stunAddr, received := stunListener(t)

		pageURL := "http://www.acme.test:" + port + "/opener?stun=" + url.QueryEscape(stunAddr)
		ctx, session := openEnforcing(t, pageURL)

		_, err := session.Navigate(ctx, pageURL)
		r.NoError(err)
		r.NoError(session.Click(ctx, "[data-testid=open-popup]"))

		title, err := pollTitleFor(ctx, session, 10*time.Second,
			func(title string) bool { return strings.HasPrefix(title, "opened:") })
		r.NoError(err)
		r.Equal("opened:null", title)

		time.Sleep(popupSettleTime)
		r.Zero(received.Load(), "the private STUN listener never saw a packet")
	})

	t.Run("enforcing: a target=_blank popup is closed and poisons the session", func(t *testing.T) {
		r := require.New(t)
		stunAddr, received := stunListener(t)

		pageURL := "http://www.acme.test:" + port + "/opener?stun=" + url.QueryEscape(stunAddr)
		ctx, session := openEnforcing(t, pageURL)

		_, err := session.Navigate(ctx, pageURL)
		r.NoError(err)

		if err := session.Click(ctx, "[data-testid=popup-link]"); err != nil {
			r.ErrorIs(err, errPopupRefused)
		}

		r.Eventually(func() bool {
			_, err := session.Evaluate(ctx, "document.title")

			return errors.Is(err, errPopupRefused)
		}, 10*time.Second, 50*time.Millisecond, "the popup poisons the session")

		time.Sleep(popupSettleTime)
		r.Zero(received.Load(), "the private STUN listener never saw a packet")
	})
}
