# Go Screenshot Tools Comparison

Comparison of Go-based tools for capturing website screenshots, deployable as containers.

## Architecture & Design

| Dimension | chromedp | Rod | gowitness | gochro |
|---|---|---|---|---|
| **CDP approach** | DOM node ID based | Remote object ID (Puppeteer-like) | Uses chromedp or Rod underneath | Chromium headless wrapper |
| **Event model** | Single loop, fixed-size buffer | Multiplexed via `goob` (no blocking) | Inherited from driver | Simple HTTP req/response |
| **Memory strategy** | In-memory DOM caching, full JSON decode of all messages | Decode-on-demand, no DOM caching | Depends on driver choice | Minimal (service wrapper) |
| **Concurrency** | Channel-based serialization; fixed buffer can deadlock under load | Thread-safe by design; no bottleneck | Configurable thread count | Standard Go HTTP goroutines |

Rod's decode-on-demand approach and multiplexed events give it a clear edge for high-concurrency workloads. chromedp's fixed-size event buffer is a known pain point at scale.

## API & Developer Experience

### chromedp (~15 lines)
```go
ctx, cancel := chromedp.NewContext(context.Background())
defer cancel()
var buf []byte
if err := chromedp.Run(ctx,
    chromedp.Navigate("https://example.com"),
    chromedp.FullScreenshot(&buf, 90),
); err != nil {
    log.Fatal(err)
}
os.WriteFile("screenshot.png", buf, 0o644)
```

### Rod (~5 lines)
```go
browser := rod.New().MustConnect()
defer browser.MustClose()
page := browser.MustPage("https://example.com").MustWaitLoad()
page.MustScreenshot("screenshot.png")
```

### gowitness (CLI)
```bash
gowitness scan single --url "https://example.com" --write-db
```

### gochro (HTTP)
```bash
curl 'http://127.0.0.1:8080/screenshot?url=https://example.com&w=1024&h=768' > screenshot.png
```

Rod's dual API (`Must*` for scripts, regular methods returning errors for production) is the most ergonomic. chromedp's action-chain pattern is more verbose but familiar to Go developers.

## Feature Matrix

| Feature | chromedp | Rod | gowitness | gochro |
|---|---|---|---|---|
| Full-page screenshots | Yes | Yes | Yes | Yes |
| Element screenshots | Yes | Yes | No | No |
| Custom viewport | Yes | Yes | Yes | Yes |
| JS execution before capture | Yes | Yes | Yes | No |
| Wait conditions | Multiple types | Rich set (idle, stable, selector) | Configurable delay | No |
| PDF generation | Yes | Yes | No | Yes |
| Cookie/header injection | Yes | Yes | Yes | No |
| Proxy support | Yes | Yes | Yes | Yes |
| Screenshot format | PNG, JPEG | PNG, JPEG | JPEG (default), PNG | PNG |
| Auto browser management | No | Yes (auto-download) | Bundled | Bundled |
| Zombie process cleanup | Manual (`--init`) | Automatic (Leakless) | Inherited | Manual (`--init`) |
| Retry helpers | No | Built-in | No | No |

## Docker & Container Deployment

| Dimension | chromedp | Rod | gowitness | gochro |
|---|---|---|---|---|
| **Official image** | `chromedp/headless-shell` | `ghcr.io/go-rod/rod` | `ghcr.io/sensepost/gowitness` | `firefart/gochro` |
| **`--shm-size` needed** | Yes (2G) | Yes (recommended) | Not documented | No (uses seccomp) |
| **`--init` needed** | Yes | No (Leakless handles it) | Inherited | Yes |
| **Seccomp profile** | Optional | Optional | Internal | Required |
| **Multi-browser support** | Via RemoteAllocator | Yes (launcher.Manager on :7317) | Internal | Single instance |
| **Remote management** | Yes (:9222 CDP) | Yes (:7317 + customizable flags per connection) | No | No |

Rod's `launcher.Manager` is notable: a single container can spawn and manage multiple browser instances dynamically, with per-connection browser flags. This makes it suitable for multi-tenant screenshot services.

## Reliability & Production Readiness

| Dimension | chromedp | Rod | gowitness | gochro |
|---|---|---|---|---|
| **GitHub stars** | 12,855 | 6,814 | 4,197 | 67 |
| **Open/closed issues** | 171 / 1,188 | 202 / 792 | 32 / 187 | 2 / 5 |
| **Contributors** | 30+ | 30+ | 30+ | 3 |
| **Last activity** | 2026-03-21 | 2026-02-17 | 2026-01-21 | 2026-03-10 |
| **License** | MIT | MIT | GPL-3.0 | MIT |
| **Chrome crash recovery** | Manual (context cancel) | Leakless auto-kills orphans | Inherited | Docker restart policy |
| **Known production users** | Widely adopted | LambdaTest, scraping at scale | SensePost / Orange Cyberdefense | wpscan.io (historical) |

## Recommendations for SolidPing

Given that we'd be integrating screenshot capture into a Go monitoring backend:

1. **Rod** is the strongest choice overall:
   - Best concurrency model (critical for a monitoring service handling many checks)
   - Lower memory per tab
   - Automatic zombie cleanup (Leakless)
   - Simplest API with production-ready error handling
   - MIT license
   - `launcher.Manager` makes container deployment flexible

2. **chromedp** if we want the largest ecosystem and most community resources, but be prepared to work around the event-loop bottleneck at scale.

3. **gowitness** is not a great fit: security-recon focused and GPL-3.0 (copyleft).

4. **gochro** could work as a quick sidecar, but too limited (no JS execution, no wait conditions, no cookies) for meaningful website monitoring screenshots.

## References

- [chromedp](https://github.com/chromedp/chromedp) - GitHub
- [Rod](https://github.com/go-rod/rod) - GitHub
- [gowitness](https://github.com/sensepost/gowitness) - GitHub
- [gochro](https://github.com/firefart/gochro) - GitHub
- [Rod documentation](https://go-rod.github.io/)
- [chromedp examples](https://github.com/chromedp/examples)
