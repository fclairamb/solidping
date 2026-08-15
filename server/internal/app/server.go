// Package app provides the HTTP server and application setup.
package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	k8sclient "k8s.io/client-go/kubernetes"

	"github.com/fclairamb/solidping/server/internal/analytics"
	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes"
	"github.com/fclairamb/solidping/server/internal/checkworker"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/checkworker/scheduling"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/dbfault"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	entitlementsapi "github.com/fclairamb/solidping/server/internal/entitlements"
	agentsadmin "github.com/fclairamb/solidping/server/internal/handlers/agents"
	"github.com/fclairamb/solidping/server/internal/handlers/agentws"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checkchannels"
	"github.com/fclairamb/solidping/server/internal/handlers/checkdependencies"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/checkjobs"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checks/importers"
	"github.com/fclairamb/solidping/server/internal/handlers/checktypes"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/emailcheck"
	"github.com/fclairamb/solidping/server/internal/handlers/emailpreview"
	"github.com/fclairamb/solidping/server/internal/handlers/emailsuppressions"
	"github.com/fclairamb/solidping/server/internal/handlers/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/escalationpolicies"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/features"
	"github.com/fclairamb/solidping/server/internal/handlers/feedback"
	"github.com/fclairamb/solidping/server/internal/handlers/files"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/localfs"
	"github.com/fclairamb/solidping/server/internal/handlers/filestorage/s3fs"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/handlers/incidentnotifications"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/integrations"
	"github.com/fclairamb/solidping/server/internal/handlers/jobs"
	"github.com/fclairamb/solidping/server/internal/handlers/jobsadmin"
	"github.com/fclairamb/solidping/server/internal/handlers/labels"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	"github.com/fclairamb/solidping/server/internal/handlers/members"
	"github.com/fclairamb/solidping/server/internal/handlers/oncallschedules"
	"github.com/fclairamb/solidping/server/internal/handlers/orglogo"
	"github.com/fclairamb/solidping/server/internal/handlers/publicconfig"
	"github.com/fclairamb/solidping/server/internal/handlers/realtimews"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/handlers/severities"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/handlers/telegramcb"
	"github.com/fclairamb/solidping/server/internal/handlers/testapi"
	"github.com/fclairamb/solidping/server/internal/handlers/twiliocb"
	"github.com/fclairamb/solidping/server/internal/handlers/unsubscribe"
	"github.com/fclairamb/solidping/server/internal/handlers/usernotifications"
	webpushhandler "github.com/fclairamb/solidping/server/internal/handlers/webpush"
	"github.com/fclairamb/solidping/server/internal/handlers/whatsappcb"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/httpx"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
	"github.com/fclairamb/solidping/server/internal/integrations/msteams"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/integrations/sshtunnel"
	"github.com/fclairamb/solidping/server/internal/jmap"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/jobs/jobworker"
	"github.com/fclairamb/solidping/server/internal/mcp"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/oauth"
	"github.com/fclairamb/solidping/server/internal/profiler"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/realtime"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/tlsedge"
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/internal/version"
	webpushpkg "github.com/fclairamb/solidping/server/internal/webpush"
	"github.com/fclairamb/solidping/server/test/testdata"
)

const (
	// embeddedPostgresPort is the default port for embedded PostgreSQL.
	embeddedPostgresPort = 5433

	// Content type constants for static file serving.
	contentTypeCSS  = "text/css"
	contentTypeJS   = "application/javascript"
	contentTypeSVG  = "image/svg+xml"
	contentTypeHTML = "text/html"
	contentTypePNG  = "image/png"
	contentTypeICO  = "image/x-icon"
)

// dbTypeSQLiteMemory is the config value selecting the ephemeral in-memory
// SQLite backend — the database every server-level test runs against.
const dbTypeSQLiteMemory = "sqlite-memory"

// ErrUnsupportedDatabaseType is returned when an unsupported database type is specified.
var ErrUnsupportedDatabaseType = errors.New("unsupported database type")

//go:embed all:res
var resFiles embed.FS

//go:embed all:dash0res
var dash0Files embed.FS

//go:embed all:status0res
var status0Files embed.FS

//go:embed openapi/*
var openAPIFiles embed.FS

//go:embed all:docsres
var docsFiles embed.FS

// Server is the HTTP server for the SolidPing application.
type Server struct {
	dbService             db.Service
	jobSvc                jobsvc.Service
	services              *services.Registry
	router                *httpx.Router
	config                *config.Config
	authService           *auth.Service
	mcpHandler            *mcp.Handler
	profilerSrv           *profiler.Server
	jmapManager           *jmap.Manager
	slackSocketSupervisor *slack.SlackSocketSupervisor
	rateLimiter           *middleware.RateLimiter // For the /api/mgmt/limits introspection handler
	realtimeHub           *realtime.Hub           // Live hint stream fan-out (nil when realtime disabled)
	statusPagesService    *statuspages.Service    // Public status-page lookups for status0 OG-metadata injection
	customDomainCache     *customDomainCache      // host -> status-page resolution cache for custom-domain routing
	tlsEdge               *tlsedge.Edge           // in-server ACME/TLS listeners (nil unless acme.enabled)
	status0FS             fs.FS                   // overridden in tests; nil means use the real embedded status0Files
	cancelCtx             context.CancelFunc
	workersWg             sync.WaitGroup // Tracks workers

	// dbFault latches the first structural database fault (the schema this
	// process needs is gone). Armed in Start to trigger a graceful shutdown:
	// no retry can bring a missing table back, and exiting lets the supervisor
	// restart us, which re-runs migrations and may fix it outright. Also flips
	// /api/mgmt/health to 503 for the shutdown window, so an orchestrator sees
	// the state even before the process is gone. See spec 2026-08-12-05.
	dbFault *dbfault.Latch
}

// NewServer creates a new HTTP server instance.
//
//nolint:funlen,cyclop // Server setup requires multiple service initializations
func NewServer(ctx context.Context, cfg *config.Config) (*Server, error) {
	var (
		dbService db.Service
		err       error
	)

	// Install the process-wide password-hashing policy from config. Parameters
	// are already validated by cfg.Validate() at startup, so a failure here is a
	// genuinely unknown algorithm and must abort (never silently fall back).
	pwPolicy, err := passwords.PolicyFromConfig(&cfg.Auth)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve password hashing policy: %w", err)
	}
	passwords.SetDefaultPolicy(pwPolicy)

	// Initialize database service based on configuration

	switch cfg.Database.Type {
	case "postgres":
		dbService, err = postgres.New(ctx, &postgres.Config{
			DSN:             cfg.Database.URL,
			Embedded:        false,
			LogSQL:          cfg.Database.LogSQL,
			RunMode:         cfg.RunMode,
			Reset:           cfg.Database.Reset,
			MaxOpenConns:    cfg.Database.MaxOpenConns,
			MaxIdleConns:    cfg.Database.MaxIdleConns,
			ConnMaxLifetime: cfg.Database.ConnMaxLifetime,
			ConnMaxIdleTime: cfg.Database.ConnMaxIdleTime,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL service: %w", err)
		}
	case "postgres-embedded":
		dbService, err = postgres.New(ctx, &postgres.Config{
			Embedded: true,
			Port:     embeddedPostgresPort,
			LogSQL:   cfg.Database.LogSQL,
			RunMode:  cfg.RunMode,
			Reset:    cfg.Database.Reset,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create embedded PostgreSQL service: %w", err)
		}
	case "sqlite":
		dbService, err = sqlite.New(ctx, sqlite.Config{
			DataDir:  cfg.Database.Dir,
			InMemory: false,
			LogSQL:   cfg.Database.LogSQL,
			RunMode:  cfg.RunMode,
			Reset:    cfg.Database.Reset,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create SQLite service: %w", err)
		}
	case dbTypeSQLiteMemory:
		dbService, err = sqlite.New(ctx, sqlite.Config{
			InMemory: true,
			LogSQL:   cfg.Database.LogSQL,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create SQLite in-memory service: %w", err)
		}
	default:
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedDatabaseType, cfg.Database.Type)
	}

	// Initialize services
	svcList := services.NewRegistry()
	svcList.Clock = clock.Real{}

	// Create check notifier based on database type — must be created before the
	// job service so its LISTEN channel can wake up GetJobWait immediately on
	// Postgres when a job is inserted via NOTIFY jobs.
	var connString string
	switch cfg.Database.Type {
	case "postgres":
		connString = cfg.Database.URL
	case "postgres-embedded":
		//nolint:lll // DSN string clarity over line length
		connString = fmt.Sprintf("postgres://postgres:postgres@localhost:%d/solidping_test?sslmode=disable", embeddedPostgresPort)
	default:
		// SQLite and others will use LocalEventNotifier, connection string not needed
		connString = ""
	}

	eventNotifier, err := notifier.New(dbService.DB(), cfg.Database.Type, connString, slog.Default())
	if err != nil {
		return nil, fmt.Errorf("failed to create event notifier: %w", err)
	}
	svcList.EventNotifier = eventNotifier

	// Realtime hint publisher: write paths (results, incidents, jobs) publish
	// org-scoped dirty hints through it onto the notifier bus. Left nil when
	// the feature is disabled — every publish site is nil-safe.
	if cfg.Realtime.Enabled {
		svcList.Realtime = realtime.NewPublisher(ctx, eventNotifier, cfg.Realtime.FlushInterval, slog.Default())
	}

	// GetJobWait subscribes internally via notifier.Listen("job.created") on each
	// call (and Unlistens on return, so subscriptions do not leak), so no external
	// wakeup channel is needed here.
	jobService := jobsvc.NewService(dbService.DB(), dbService, eventNotifier, svcList.Realtime)
	svcList.Jobs = jobService

	checkJobService := checkjobsvc.NewService(dbService.DB())
	svcList.CheckJobs = checkJobService

	// Create email services
	emailSender := email.NewSender(&cfg.Email, slog.Default())
	svcList.EmailSender = emailSender

	emailFormatter, err := email.NewFormatter()
	if err != nil {
		return nil, fmt.Errorf("failed to create email formatter: %w", err)
	}
	svcList.EmailFormatter = emailFormatter

	// Credentials encryption service. The master key comes from env (or a
	// file mount) — never persisted server-side. With no key configured,
	// .Enabled() is false and write paths fall back to plaintext storage.
	credSvc, err := BuildCredentialsService(cfg, dbService)
	if err != nil {
		return nil, fmt.Errorf("init credentials service: %w", err)
	}
	svcList.Credentials = credSvc

	if !credSvc.Enabled() {
		// Plaintext-fallback warning at startup so an unconfigured prod
		// install can be spotted in logs.
		//
		//nolint:sloglint // startup-only, no request context
		slog.Warn("credentials encryption disabled — secrets stored in plaintext",
			"how_to_fix", "set SP_ENCRYPTION_MASTER_KEY (or SP_ENCRYPTION_MASTER_KEY_FILE)")
	}

	// Wire the freebox_line checker's connection resolver. The resolver
	// owns the DB lookup + app_token decrypt so the checker package stays
	// importable from unit tests without a live database. Mirrors the
	// checkjs.ResolveChecker indirection pattern set up by the registry.
	checkfreeboxline.ConnectionResolverFunc = newFreeboxConnectionResolver(dbService, credSvc)

	// Wire the kubernetes checker's clientset resolver. Same indirection: the
	// resolver owns the DB lookup + credential decrypt (via the integrations
	// kubernetes package's single chokepoint) so the checker package stays
	// importable from unit tests without a live database or cluster.
	checkkubernetes.ClientsetResolverFunc = func(ctx context.Context, clusterUID string) (k8sclient.Interface, error) {
		return integrationk8s.ResolveClientsetByUID(ctx, dbService, credSvc, clusterUID)
	}

	// Wire the SSH-tunnel resolver used by tunnel-capable checks
	// (`tunnelCheckUid` in their config). Same indirection again: it owns the
	// check lookup + credential decrypt so the checkers only ever consume a
	// dialer from their context.
	sshtunnel.ResolverFunc = sshtunnel.NewResolver(dbService, credSvc)

	// Initialize Sentry error tracking
	if err := initSentry(cfg.Sentry); err != nil {
		return nil, fmt.Errorf("failed to initialize Sentry: %w", err)
	}

	// Entitlements service. Defaults are deployment-mode dependent
	// (self-hosted caps SSO; SaaS caps check rate). Stale-after of zero
	// disables billing-service stale fallback — fine for both deployment
	// modes today, the billing integration can flip it later via system
	// parameters.
	entitlementsService := entitlementsapi.NewService(
		dbService, entitlementsapi.DefaultsFor(cfg.Deployment.Mode), 0,
		entitlementsapi.WithRunawayCaps(
			cfg.Entitlements.SMSRunawayPerHour, cfg.Entitlements.CallRunawayPerHour,
		),
		entitlementsapi.WithWhatsAppRunawayCap(cfg.Entitlements.WhatsAppRunawayPerHour),
		entitlementsapi.WithTelegramRunawayCap(cfg.Entitlements.TelegramRunawayPerHour),
	)
	svcList.Entitlements = entitlementsService

	// Create auth service. The entitlements service gates SSO membership
	// caps inside JoinOrgViaLogin (every OAuth/SAML/LDAP callback) and inside
	// autoJoinMatchingOrgs.
	authService := auth.NewService(dbService, cfg.Auth, cfg, jobService, entitlementsService)

	// Register file storage backends. Idempotent — safe to call once at startup.
	localfs.Register()
	s3fs.Register()

	// Initialize VAPID keys for Web Push. Auto-generates and persists to
	// app_settings when not pre-provisioned via env vars.
	if pub, priv, err := webpushpkg.GetOrCreateVAPIDKeys(ctx, webpushpkg.Config{
		VAPIDPublicKey:  cfg.WebPush.VAPIDPublicKey,
		VAPIDPrivateKey: cfg.WebPush.VAPIDPrivateKey,
		Subject:         cfg.WebPush.Subject,
		Enabled:         cfg.WebPush.Enabled,
	}, dbService); err != nil {
		slog.WarnContext(ctx, "webpush: VAPID key initialization failed — web push disabled", "err", err)
	} else {
		cfg.WebPush.VAPIDPublicKey = pub
		cfg.WebPush.VAPIDPrivateKey = priv
		svcList.WebPushOptions = webpushpkg.Options{
			VAPIDPublicKey:  pub,
			VAPIDPrivateKey: priv,
			Subject:         cfg.WebPush.Subject,
		}
	}

	server := &Server{
		dbService:         dbService,
		jobSvc:            jobService,
		services:          svcList,
		config:            cfg,
		authService:       authService,
		profilerSrv:       profiler.New(&cfg.Profiler),
		customDomainCache: newCustomDomainCache(customDomainCacheTTL),
		dbFault:           dbfault.NewLatch(slog.Default()),
	}

	return server, nil
}

// registerSubsystemMetrics wires the A2 subsystem-size collector to the live
// services (DEK cache, event listeners) and rate limiter. Reads are taken at
// scrape time via closures, so the collector holds no copies and costs nothing
// at idle. Guards each source for nil so a partially-initialized server (or a
// future role that skips a subsystem) degrades to 0 rather than panicking.
func (s *Server) registerSubsystemMetrics(reg prometheus.Registerer) {
	sizes := prommetrics.SubsystemSizes{}

	if s.services != nil {
		if cred := s.services.Credentials; cred != nil {
			sizes.DEKCacheEntries = cred.DEKCacheLen
		}
		if ev := s.services.EventNotifier; ev != nil {
			sizes.EventListeners = func() int { return notifier.ListenerCount(ev) }
		}
	}
	if s.rateLimiter != nil {
		sizes.RateLimitEntries = s.rateLimiter.EntryCount
	}

	prommetrics.RegisterSubsystems(reg, sizes)
}

// deviceConsentRequestsPerMinute is the per-caller allowance for the RFC 8628
// consent endpoints. A human approving a device login makes a handful of
// requests; anything beyond this is someone walking the user-code space.
const deviceConsentRequestsPerMinute = 20

// deviceConsentRateLimitConfig derives a much stricter limiter config from the
// server's own, keeping deployment-specific knobs (trusted proxy hops, token
// bucket keying) while collapsing the allowance. RateQueue is left at zero so
// an over-eager caller is rejected outright rather than parked in a waiting
// room, and the concurrency limiter is disabled (the global one already
// applies).
func deviceConsentRateLimitConfig(base config.RateLimitConfig) config.RateLimitConfig {
	return config.RateLimitConfig{
		RequestsPerMinute: deviceConsentRequestsPerMinute,
		Burst:             deviceConsentRequestsPerMinute / 2,
		TrustedProxies:    base.TrustedProxies,
		TokenBucketsPerIP: base.TokenBucketsPerIP,
	}
}

// SetupRoutes builds the HTTP router and registers every handler. It must
// be called after InitializeSystemConfig so handlers see the post-overlay
// config (e.g. PasskeyService deriving its RP ID from cfg.Server.BaseURL,
// which the system-parameters table can override at runtime).
//
//nolint:funlen,cyclop // Route registration function naturally grows with new routes
func (s *Server) SetupRoutes(ctx context.Context) {
	// Install the process-wide product-analytics client (spec 2026-08-02-08).
	// Done here rather than in NewServer because SetupRoutes runs *after*
	// InitializeSystemConfig, so DB-stored posthog.* parameters are already
	// overlaid onto cfg. When PostHog is not configured this installs a genuine
	// no-op: no client, no goroutine, no network.
	analytics.SetDefault(analytics.New(s.config.PostHog))
	if analytics.Default().Enabled() {
		slog.InfoContext(ctx, "Product analytics enabled", "host", s.config.PostHog.ResolvedHost())
	}

	// Derive the Telegram bot username and webhook secret the operator did not
	// have to supply. SYNCHRONOUS and here on purpose: it WRITES cfg.Telegram,
	// which every handler below reads concurrently, so it must complete before
	// a single route exists — doing it from the async bootstrapTelegram
	// goroutine would be a real data race. It also has to precede that
	// goroutine's setWebhook, or Telegram could end up holding a secret no pod
	// in the fleet knows. No-op unless Telegram is configured.
	resolveTelegramSettings(ctx, s.dbService, s.config)

	router := httpx.New()
	rateLimiter := middleware.NewRateLimiter(s.config.Server.RateLimiting, ctx)
	s.rateLimiter = rateLimiter
	// ctx for RequestTimeout is taken from each request's context inside the
	// middleware closure, not threaded through here; the contextcheck linter
	// can't see that.
	timeoutMW := middleware.RequestTimeout(s.config.Server.MaxRequestDuration)
	mainGroup := router.Use(s.corsMiddleware).Use(middleware.SentryMiddleware()).Use(s.loggingMiddleware).
		Use(middleware.HTTPMetrics).
		Use(timeoutMW).
		Use(rateLimiter.RateLimit).
		Use(rateLimiter.ConcurrencyLimit)

	// API routes
	api := mainGroup.NewGroup("/api/v1")
	api.OPTIONS("/*path", func(_ http.ResponseWriter, req *http.Request) error {
		slog.InfoContext(req.Context(), "OPTIONS request", "path", req.URL.Path)
		return nil
	})

	// Create auth handler and middleware
	authHandler := auth.NewHandler(s.authService, s.config)
	passkeyService := auth.NewPasskeyService(s.authService, s.dbService)
	passkeyHandler := auth.NewPasskeyHandler(passkeyService, base.NewHandlerBase(s.config))
	authMiddleware := middleware.NewAuthMiddleware(s.authService, s.dbService, s.config)

	// Root-level auth routes (public, no authentication required)
	rootAuth := api.NewGroup("/auth")
	rootAuth.POST("/login", authHandler.Login)
	rootAuth.POST("/refresh", authHandler.Refresh)
	rootAuth.POST("/register", authHandler.Register)
	rootAuth.POST("/confirm-registration", authHandler.ConfirmRegistration)
	rootAuth.POST("/request-password-reset", authHandler.RequestPasswordReset)
	rootAuth.POST("/reset-password", authHandler.ResetPassword)
	rootAuth.GET("/invite/:token", authHandler.GetInviteInfo)
	rootAuth.POST("/accept-invite", authHandler.AcceptInvite)
	rootAuth.POST("/2fa/verify", authHandler.Verify2FA)
	rootAuth.POST("/2fa/recovery", authHandler.Recovery2FA)
	rootAuth.POST("/passkeys/login/begin", passkeyHandler.LoginBegin)
	rootAuth.POST("/passkeys/login/finish", passkeyHandler.LoginFinish)

	// OAuth 2.0 Device Authorization Grant, RFC 8628 (spec 2026-08-08-02).
	// Both endpoints below are public: opening a request grants nothing until
	// a logged-in human approves it, and the token endpoint is guarded by the
	// device_code's 32 bytes of entropy plus its own slow_down interval — a
	// legitimate client polls it every few seconds for the request's TTL, so a
	// stricter per-IP limit would break the flow rather than protect it.
	rootAuth.POST("/device", authHandler.StartDeviceAuthorization)
	rootAuth.POST("/device/token", authHandler.PollDeviceToken)

	// Root-level auth routes (protected, authentication required)
	rootAuthProtected := rootAuth.Use(authMiddleware.RequireAuth)
	rootAuthProtected.POST("/logout", authHandler.Logout)
	rootAuthProtected.POST("/switch-org", authHandler.SwitchOrg)
	rootAuthProtected.GET("/me", authHandler.Me)
	rootAuthProtected.PATCH("/me", authHandler.UpdateMe)
	rootAuthProtected.GET("/tokens", authHandler.GetAllUserTokens)
	rootAuthProtected.POST("/2fa/setup", authHandler.Setup2FA)
	rootAuthProtected.POST("/2fa/confirm", authHandler.Confirm2FA)
	rootAuthProtected.DELETE("/2fa", authHandler.Disable2FA)
	// Static /tokens/current is registered ahead of the /tokens/:tokenUid param
	// route; chi matches the static segment first (as with
	// /passkeys/register/begin vs /passkeys/:uid). It revokes the caller's own
	// grant (Claims.RefreshUID) — the bearer-only self-revocation for a client
	// that no longer holds its refresh token.
	rootAuthProtected.DELETE("/tokens/current", authHandler.RevokeCurrentToken)
	rootAuthProtected.DELETE("/tokens/:tokenUid", authHandler.RevokeToken)
	rootAuthProtected.POST("/passkeys/register/begin", passkeyHandler.RegisterBegin)
	rootAuthProtected.POST("/passkeys/register/finish", passkeyHandler.RegisterFinish)
	rootAuthProtected.GET("/passkeys", passkeyHandler.List)
	rootAuthProtected.PATCH("/passkeys/:uid", passkeyHandler.Rename)
	rootAuthProtected.DELETE("/passkeys/:uid", passkeyHandler.Delete)
	// Device-authorization consent (RFC 8628 §3.3). Unlike the two public
	// device endpoints, these are keyed by the SHORT user code — the
	// brute-forceable surface — so they get a dedicated, much stricter
	// instance of the same rate-limit middleware on top of the global one.
	deviceConsentLimiter := middleware.NewRateLimiter(deviceConsentRateLimitConfig(s.config.Server.RateLimiting), ctx)
	deviceConsent := rootAuthProtected.Use(deviceConsentLimiter.RateLimit)
	deviceConsent.GET("/device/consent", authHandler.GetDeviceConsent)
	deviceConsent.POST("/device/consent", authHandler.RespondToDeviceConsent)

	rootAuthProtected.POST("/membership-requests", authHandler.CreateMembershipRequestHandler)
	rootAuthProtected.GET("/membership-requests", authHandler.ListOwnMembershipRequestsHandler)
	rootAuthProtected.DELETE("/membership-requests/:uid", authHandler.CancelMembershipRequestHandler)

	// orgGroup builds an org-scoped route group guarded by RequireAuth +
	// RequireOrgAccess. Every /orgs/:org/* group MUST be created through this
	// helper rather than api.NewGroup(...).Use(RequireAuth): org authorization
	// is then structural, so a new org route can't silently ship without the
	// membership check the way ~30 groups did before spec 2026-07-15-01 (a
	// cross-tenant leak — RequireAuth never reads :org, so any authenticated
	// user could read any other org's checks, incidents and member emails).
	//
	// Intentionally public /orgs/:org/* routes (status-page badges, the
	// magic-link incident ack, the realtime WS, the public status-page
	// subscribe) are registered directly on `api` and deliberately do NOT go
	// through here; the service-token entitlements group keeps its own
	// ServiceTokenBypass -> RequireAuth -> RequireOrgAccess chain.
	//
	// orgSlugRedirect runs FIRST in the chain: a request to an organization's
	// previous slug (after a rename) is answered with a permanent redirect to
	// the current slug before the token-scope check in AuthorizeOrgAccess can
	// turn it into a 403 (spec 2026-08-08-12).
	//
	// A handful of /orgs/:org/* groups cannot use this helper because they need
	// an extra gate (RequireOrgAdmin) or a different auth chain entirely (the
	// entitlements service-signature chain). Those MUST still list
	// orgSlugRedirect.Middleware first in their own Use(...) — forgetting it is
	// invisible in review and only ever breaks API/CLI clients holding an old
	// slug, since dash0's own SPA redirect masks it. That is not left to
	// discipline: TestEveryOrgScopedAPIRouteRedirectsOnAPreviousSlug walks the
	// real route table and fails on any org-scoped route that does not redirect
	// (the realtime WS is the single documented exception).
	orgSlugRedirect := middleware.NewOrgSlugRedirect(s.dbService)
	orgGroup := func(path string) *httpx.Group {
		return api.NewGroup(path).
			Use(orgSlugRedirect.Middleware, authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
	}

	// publicOrgAPI carries the previous-slug redirect for the intentionally
	// public /orgs/:org/... and /status-pages/:org/... routes, which bypass
	// orgGroup by design. These are exactly the URLs customers paste into
	// their own sites (status pages, SVG badges, the /embed/v1 widget's
	// summary endpoint), so they are the ones a rename must not break.
	publicOrgAPI := api.Use(orgSlugRedirect.Middleware)

	// Org creation (protected). Any authenticated user may create an org; the
	// creator becomes its owner (spec 2026-08-08-11).
	orgsGroup := api.NewGroup("/orgs").Use(authMiddleware.RequireAuth)
	orgsGroup.POST("", authHandler.CreateOrg)

	// Org deletion — owner only, enforced server-side by RequireOrgOwner (an
	// admin is refused with the standard 403 shape, not merely denied the
	// button in the dashboard).
	orgOwnerGroup := api.NewGroup("/orgs/:org").
		Use(orgSlugRedirect.Middleware,
			authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgOwner)
	orgOwnerGroup.DELETE("", authHandler.DeleteOrg)

	// Org profile (name / slug / logo) — owner only, same rationale as delete:
	// renaming the slug moves every URL of the organization (spec 2026-08-08-12).
	orgOwnerGroup.PATCH("", authHandler.UpdateOrgProfile)

	// Org-scoped token management (protected)
	orgTokens := orgGroup("/orgs/:org/tokens")
	orgTokens.GET("", authHandler.GetOrgTokens)
	orgTokens.POST("", authHandler.CreateToken)

	// Org invitations (protected, admin-only checked in handler)
	orgInvitations := orgGroup("/orgs/:org/invitations")
	orgInvitations.GET("", authHandler.ListInvitations)
	orgInvitations.POST("", authHandler.CreateInvitation)
	orgInvitations.DELETE("/:uid", authHandler.RevokeInvitation)

	// Org settings (protected, admin-only checked in handler)
	orgSettings := orgGroup("/orgs/:org/settings")
	orgSettings.GET("", authHandler.GetOrgSettings)
	orgSettings.PATCH("", authHandler.UpdateOrgSettings)

	// Org membership requests (protected, admin-only checked in handler)
	orgMembershipRequests := orgGroup("/orgs/:org/membership-requests")
	orgMembershipRequests.GET("", authHandler.ListOrgMembershipRequestsHandler)
	orgMembershipRequests.POST("/:uid/approve", authHandler.ApproveMembershipRequestHandler)
	orgMembershipRequests.POST("/:uid/reject", authHandler.RejectMembershipRequestHandler)

	// Slack OAuth routes (org-independent, public)
	if s.config.Slack.Enabled && s.config.Slack.ClientID != "" {
		slackOAuthService := auth.NewSlackOAuthService(s.dbService, s.config, s.authService)
		slackOAuthHandler := auth.NewSlackOAuthHandler(slackOAuthService, s.config)
		slackAuth := api.NewGroup("/auth/slack")
		slackAuth.GET("/login", slackOAuthHandler.Login)
		slackAuth.GET("/callback", slackOAuthHandler.Callback)
		slackAuth.POST("/exchange", slackOAuthHandler.Exchange)
	}

	// Google OAuth routes (org-scoped, public)
	if s.config.Google.Enabled && s.config.Google.ClientID != "" {
		googleOAuthService := auth.NewGoogleOAuthService(s.dbService, s.config, s.authService)
		googleOAuthHandler := auth.NewGoogleOAuthHandler(googleOAuthService, s.config)
		googleAuth := api.NewGroup("/auth/google")
		googleAuth.GET("/login", googleOAuthHandler.Login)
		googleAuth.GET("/callback", googleOAuthHandler.Callback)
	}

	// GitHub OAuth routes (org-scoped, public)
	if s.config.GitHub.Enabled && s.config.GitHub.ClientID != "" {
		gitHubOAuthService := auth.NewGitHubOAuthService(s.dbService, s.config, s.authService)
		gitHubOAuthHandler := auth.NewGitHubOAuthHandler(gitHubOAuthService, s.config)
		gitHubAuth := api.NewGroup("/auth/github")
		gitHubAuth.GET("/login", gitHubOAuthHandler.Login)
		gitHubAuth.GET("/callback", gitHubOAuthHandler.Callback)
	}

	// Microsoft OAuth routes (org-scoped, public)
	if s.config.Microsoft.Enabled && s.config.Microsoft.ClientID != "" {
		microsoftOAuthService := auth.NewMicrosoftOAuthService(s.dbService, s.config, s.authService)
		microsoftOAuthHandler := auth.NewMicrosoftOAuthHandler(microsoftOAuthService, s.config)
		microsoftAuth := api.NewGroup("/auth/microsoft")
		microsoftAuth.GET("/login", microsoftOAuthHandler.Login)
		microsoftAuth.GET("/callback", microsoftOAuthHandler.Callback)
	}

	// GitLab OAuth routes (org-scoped, public)
	if s.config.GitLab.Enabled && s.config.GitLab.ClientID != "" {
		gitLabOAuthService := auth.NewGitLabOAuthService(s.dbService, s.config, s.authService)
		gitLabOAuthHandler := auth.NewGitLabOAuthHandler(gitLabOAuthService, s.config)
		gitLabAuth := api.NewGroup("/auth/gitlab")
		gitLabAuth.GET("/login", gitLabOAuthHandler.Login)
		gitLabAuth.GET("/callback", gitLabOAuthHandler.Callback)
	}

	// Discord OAuth routes (org-independent, public)
	if s.config.Discord.Enabled && s.config.Discord.ClientID != "" {
		discordOAuthService := auth.NewDiscordOAuthService(s.dbService, s.config, s.authService)
		discordOAuthHandler := auth.NewDiscordOAuthHandler(discordOAuthService, s.config)
		discordAuth := api.NewGroup("/auth/discord")
		discordAuth.GET("/login", discordOAuthHandler.Login)
		discordAuth.GET("/callback", discordOAuthHandler.Callback)
	}

	// Generic OIDC routes (org-scoped, public)
	if s.config.OIDC.Enabled && s.config.OIDC.IssuerURL != "" && s.config.OIDC.ClientID != "" {
		oidcOAuthService := auth.NewOIDCOAuthService(s.dbService, s.config, s.authService)
		oidcOAuthHandler := auth.NewOIDCOAuthHandler(oidcOAuthService, s.config)
		oidcAuth := api.NewGroup("/auth/oidc")
		oidcAuth.GET("/login", oidcOAuthHandler.Login)
		oidcAuth.GET("/callback", oidcOAuthHandler.Callback)
	}

	// SAML 2.0 SP routes (org-scoped, public). Metadata is served whenever
	// SAML is enabled at all — even before an IdP is configured — since an
	// admin typically needs this SP's own metadata first, to configure their
	// IdP, before they have IdP metadata to paste back; login/ACS need the
	// IdP side configured too (checked again inside the service).
	if s.config.SAML.Enabled {
		samlService := auth.NewSAMLService(s.dbService, s.config, s.authService)
		samlHandler := auth.NewSAMLHandler(samlService, s.config)
		samlAuth := api.NewGroup("/auth/saml")
		samlAuth.GET("/login", samlHandler.Login)
		samlAuth.POST("/acs", samlHandler.ACS)
		samlAuth.GET("/metadata", samlHandler.Metadata)
	}

	// Auth providers endpoint (public)
	providersHandler := auth.NewProvidersHandler(s.config, passkeyService.Enabled)
	api.GET("/auth/providers", providersHandler.ListProviders)

	// Public, unauthenticated config blob the SPA reads at boot (spec
	// 2026-08-02-08). Non-secret values only — see the publicconfig package doc.
	publicConfigHandler := publicconfig.NewHandler(s.config)
	api.GET("/config", publicConfigHandler.GetConfig)

	// Check types service (constructed early so MCP can use it too)
	activationResolver := checkerdef.NewActivationResolver(s.config.Checkers)
	checkTypesService := checktypes.NewService(activationResolver, s.config.Server.BaseURL)

	// MCP endpoint (auth via PAT token, org derived from token)
	s.mcpHandler = mcp.NewHandler(
		s.dbService, s.services.EventNotifier, s.jobSvc, checkTypesService,
		s.services.Credentials, s.services.Entitlements, s.services.Realtime,
		s.config,
	)
	mcpGroup := api.NewGroup("/mcp")
	// GET is deliberately outside RequireMCPAuth: a browser opening the
	// endpoint has no token and gets a helpful redirect to the dashboard MCP
	// page, while an MCP client probing with Accept: text/event-stream gets
	// the spec-mandated 405 (we don't serve server-initiated SSE streams).
	mcpGroup.GET("", s.mcpHandler.HandleGet)
	mcpAuthed := mcpGroup.Use(authMiddleware.RequireMCPAuth)
	mcpAuthed.POST("", s.mcpHandler.Handle)
	mcpAuthed.DELETE("", s.mcpHandler.HandleDelete)

	// OAuth 2.1 authorization server for the MCP resource (spec
	// 2026-06-20-03). Discovery docs are served at the site root where MCP
	// clients expect them; the flow endpoints live under /api/v1/oauth so the
	// existing per-IP rate limiter (limitedPrefix = /api/v1/) covers them.
	oauthService := oauth.NewService(s.dbService, s.authService, s.config)
	oauthHandler := oauth.NewHandler(oauthService, s.config)
	mainGroup.GET(oauth.PathProtectedResourceMetadata, oauthHandler.ProtectedResourceMetadata)
	mainGroup.GET(oauth.PathAuthorizationServerMetadata, oauthHandler.AuthorizationServerMetadata)
	mainGroup.GET(oauth.PathOpenIDConfiguration, oauthHandler.AuthorizationServerMetadata)
	mainGroup.GET(oauth.PathJWKS, oauthHandler.JWKS)
	oauthGroup := api.NewGroup("/oauth")
	oauthGroup.GET("/authorize", oauthHandler.Authorize)
	oauthGroup.POST("/authorize", oauthHandler.ApproveAuthorize)
	oauthGroup.POST("/token", oauthHandler.Token)
	oauthGroup.POST("/register", oauthHandler.Register)
	oauthGroup.POST("/revoke", oauthHandler.Revoke)

	// Job routes (auth required for org-scoped routes)
	jobHandler := jobs.NewHandler(s.jobSvc)
	orgJobsGroup := api.NewGroup("/orgs/:org/jobs").
		Use(orgSlugRedirect.Middleware, authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
	orgJobsGroup.POST("", jobHandler.CreateJob)
	orgJobsGroup.GET("", jobHandler.ListJobs)
	orgJobsGroup.GET("/:uid", jobHandler.GetJob)
	orgJobsGroup.DELETE("/:uid", jobHandler.CancelJob)

	// Admin Jobs observability (spec 2026-06-15-05). Read-only views over the
	// background-jobs queue and the check-schedule (check_jobs) table. Org
	// endpoints require org admin; /system endpoints require super admin.
	checkJobsSvc := checkjobs.NewService(s.dbService.DB())
	checkJobsHandler := checkjobs.NewHandler(checkJobsSvc, s.config)
	jobsAdminSvc := jobsadmin.NewService(s.dbService.DB())
	jobsAdminHandler := jobsadmin.NewHandler(jobsAdminSvc, s.config)

	orgJobsAdmin := api.NewGroup("/orgs/:org").
		Use(orgSlugRedirect.Middleware,
			authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
	orgJobsAdmin.GET("/jobs/stats", checkJobsHandler.Stats)
	orgJobsAdmin.GET("/admin/jobs", jobsAdminHandler.ListOrgJobs)
	orgJobsAdmin.GET("/admin/jobs/:uid", jobsAdminHandler.GetOrgJob)
	orgJobsAdmin.GET("/admin/jobs/:uid/chain", jobsAdminHandler.GetOrgJobChain)
	orgJobsAdmin.GET("/check-jobs", checkJobsHandler.ListCheckJobs)
	orgJobsAdmin.GET("/check-jobs/:uid", checkJobsHandler.GetCheckJob)

	systemJobsGroup := api.NewGroup("/system").
		Use(authMiddleware.RequireAuth, authMiddleware.RequireSuperAdmin)
	systemJobsGroup.GET("/jobs/stats", checkJobsHandler.SystemStats)
	systemJobsGroup.GET("/jobs", jobsAdminHandler.ListSystemJobs)
	systemJobsGroup.GET("/jobs/:uid", jobsAdminHandler.GetSystemJob)
	systemJobsGroup.GET("/jobs/:uid/chain", jobsAdminHandler.GetSystemJobChain)
	systemJobsGroup.GET("/check-jobs", checkJobsHandler.ListSystemCheckJobs)
	systemJobsGroup.GET("/check-jobs/:uid", checkJobsHandler.GetSystemCheckJob)

	// Check types routes
	checkTypesHandler := checktypes.NewHandler(checkTypesService, s.config)
	api.GET("/check-types", checkTypesHandler.ListServerCheckTypes)      // Public, no auth
	api.GET("/check-types/samples", checkTypesHandler.ListSampleConfigs) // Public, no auth
	orgCheckTypes := orgGroup("/orgs/:org/check-types")
	orgCheckTypes.GET("", checkTypesHandler.ListOrgCheckTypes)

	// Check routes (authentication required)
	checksService := checks.NewService(
		s.dbService, s.services.EventNotifier, s.services.Credentials, s.services.Entitlements)
	checksHandler := checks.NewHandler(checksService, s.config)
	orgChecks := orgGroup("/orgs/:org/checks")
	orgChecks.GET("", checksHandler.ListChecks)
	// Aggregate counters for the org dashboard (spec 2026-08-02-06). Registered
	// here, ahead of the "/:checkUid" routes below, so the literal "stats"
	// segment is never captured as a check UID.
	orgChecks.GET("/stats", checksHandler.GetCheckStats)
	orgChecks.POST("", checksHandler.CreateCheck)

	// Config-as-code surface (export/import/apply) is admin-only: import and
	// apply mutate the whole check set, and apply can delete-by-absence.
	// Export is gated alongside them (it was RequireAuth-only — a latent gap;
	// see specs/todos/2026-06-20-05-config-as-code.md). Apply is the reconcile
	// sibling of import with dry-run/prune/deletion-cap guardrails.
	orgChecksAdmin := api.NewGroup("/orgs/:org/checks").
		Use(orgSlugRedirect.Middleware,
			authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
	orgChecksAdmin.GET("/export", checksHandler.ExportChecks)
	orgChecksAdmin.POST("/import", checksHandler.ImportChecks)
	orgChecksAdmin.POST("/apply", checksHandler.ApplyChecks)

	// Third-party importers (Gatus / Better Stack / Uptime Kuma) convert a
	// foreign config into the canonical export document and then reuse the very
	// same ApplyChecks path above — no second import pipeline.
	importersHandler := importers.NewHandler(checksService, s.config)
	importersHandler.RegisterRoutes(orgChecksAdmin)

	orgChecks.POST("/validate", checksHandler.ValidateCheck)
	orgChecks.GET("/:checkUid", checksHandler.GetCheck)
	orgChecks.PUT("/:slug", checksHandler.UpsertCheck)
	orgChecks.PATCH("/:checkUid", checksHandler.UpdateCheck)
	orgChecks.DELETE("/:checkUid", checksHandler.DeleteCheck)
	orgChecks.POST("/:checkUid/clone", checksHandler.CloneCheck)
	// Heartbeat-only: mints a fresh ping token, invalidating every existing
	// ping URL immediately (400 for non-heartbeat checks).
	orgChecks.POST("/:checkUid/rotate-token", checksHandler.RotateHeartbeatToken)

	// Network discovery routes (authentication + org access required)
	discoverySvc := discovery.NewService(
		s.dbService.DB(), s.dbService, checksService, s.jobSvc, s.services.Credentials,
	)
	discoveryHandler := discovery.NewHandler(discoverySvc, s.config)
	orgDiscovery := api.NewGroup("/orgs/:org/discovery").
		Use(orgSlugRedirect.Middleware, authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
	discoveryHandler.RegisterRoutes(orgDiscovery)

	// Label autocomplete routes
	labelsService := labels.NewService(s.dbService)
	labelsHandler := labels.NewHandler(labelsService, s.config)
	orgLabels := orgGroup("/orgs/:org/labels")
	orgLabels.GET("", labelsHandler.ListLabels)

	// Region routes
	regionsService := regionshandler.NewService(s.dbService)
	regionsHandler := regionshandler.NewHandler(regionsService, s.config)
	api.GET("/regions", regionsHandler.ListGlobalRegions) // Public, no auth
	orgRegions := orgGroup("/orgs/:org/regions")
	orgRegions.GET("", regionsHandler.ListOrgRegions)

	// Check group routes (authentication required)
	checkGroupsService := checkgroups.NewService(s.dbService)
	checkGroupsHandler := checkgroups.NewHandler(checkGroupsService, s.config)
	orgCheckGroups := orgGroup("/orgs/:org/check-groups")
	orgCheckGroups.GET("", checkGroupsHandler.ListCheckGroups)
	orgCheckGroups.POST("", checkGroupsHandler.CreateCheckGroup)
	orgCheckGroups.GET("/:uid", checkGroupsHandler.GetCheckGroup)
	orgCheckGroups.PATCH("/:uid", checkGroupsHandler.UpdateCheckGroup)
	orgCheckGroups.DELETE("/:uid", checkGroupsHandler.DeleteCheckGroup)

	// Severity routes (authentication required). Per-org channel-set
	// primitive consumed by escalation step fan-out — spec 2026-05-08-03.
	severitiesService := severities.NewService(s.dbService)
	severitiesHandler := severities.NewHandler(severitiesService, s.config)
	orgSeverities := orgGroup("/orgs/:org/severities")
	orgSeverities.GET("", severitiesHandler.ListSeverities)
	orgSeverities.POST("", severitiesHandler.CreateSeverity)
	orgSeverities.GET("/:uid", severitiesHandler.GetSeverity)
	orgSeverities.PATCH("/:uid", severitiesHandler.UpdateSeverity)
	orgSeverities.DELETE("/:uid", severitiesHandler.DeleteSeverity)

	// Check dependency routes (authentication required)
	depsService := checkdependencies.NewService(s.dbService)
	depsHandler := checkdependencies.NewHandler(depsService, s.config)
	orgChecks.GET("/:check/dependencies", depsHandler.ListForCheck)
	orgChecks.POST("/:check/dependencies", depsHandler.Create)
	orgChecks.PATCH("/:check/dependencies/:uid", depsHandler.Update)
	orgChecks.DELETE("/:check/dependencies/:uid", depsHandler.Delete)
	orgDeps := orgGroup("/orgs/:org/dependencies")
	orgDeps.GET("", depsHandler.Graph)

	// Check-channel binding routes (authentication required). Same alias
	// pattern as the org-level integrations block: `/integrations` is canonical
	// going forward, `/channels` is the prior name (the notify role). The legacy
	// `/connections` path was dropped in PR-E.
	checkChannelsService := checkchannels.NewService(s.dbService)
	checkChannelsHandler := checkchannels.NewHandler(checkChannelsService, s.config)
	for _, suffix := range []string{"/integrations", "/channels"} {
		orgChecks.GET("/:check"+suffix, checkChannelsHandler.ListChannels)
		orgChecks.PUT("/:check"+suffix, checkChannelsHandler.SetChannels)
		orgChecks.POST("/:check"+suffix+"/:connection", checkChannelsHandler.AddChannel)
		orgChecks.DELETE("/:check"+suffix+"/:connection", checkChannelsHandler.RemoveConnection)
		orgChecks.GET("/:check"+suffix+"/:connection", checkChannelsHandler.GetConnectionSettings)
		orgChecks.PATCH("/:check"+suffix+"/:connection", checkChannelsHandler.UpdateConnectionSettings)
	}

	// Badge routes (public, no authentication required)
	badgesService := badges.NewService(s.dbService, s.config)
	badgesHandler := badges.NewHandler(badgesService, s.config)
	publicOrgAPI.GET("/orgs/:org/checks/:check/badges/:components", badgesHandler.GetBadge)

	// Heartbeat ingestion routes (public, token-based auth)
	heartbeatService := heartbeat.NewService(s.dbService, s.jobSvc, s.services.Realtime)
	heartbeatHandler := heartbeat.NewHandler(heartbeatService, s.config)
	publicOrgAPI.POST("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)
	publicOrgAPI.GET("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)

	// The HTTP edge-worker API (/workers/{register,heartbeat,claim-jobs,
	// submit-result}) was removed with spec 2026-07-16-02: it had no production
	// client and its auth was a plaintext spw_ bearer token stored verbatim.
	// Deported agents now connect outbound over the WebSocket agent transport,
	// which authenticates by Ed25519 signature and never persists a usable
	// credential. The claim/submit service logic lives on and is reused by that
	// handler; the in-process worker keeps talking to checkjobsvc directly.

	// Private locations (org-private regions) + deported agents admin API.
	// Org-admin only: minting an enrollment token grants the ability to run
	// checks inside the customer's network, and revoking an agent is
	// security-relevant.
	agentsAdminSvc := agentsadmin.NewService(s.dbService, s.services.Credentials, s.services.Entitlements)
	agentsAdminHandler := agentsadmin.NewHandler(agentsAdminSvc, s.config)
	orgAgentsAdmin := api.NewGroup("/orgs/:org").
		Use(orgSlugRedirect.Middleware,
			authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
	orgAgentsAdmin.GET("/private-regions", agentsAdminHandler.ListPrivateRegions)
	orgAgentsAdmin.POST("/private-regions", agentsAdminHandler.CreatePrivateRegion)
	orgAgentsAdmin.DELETE("/private-regions/:slug", agentsAdminHandler.DeletePrivateRegion)
	orgAgentsAdmin.GET("/agent-enrollment-tokens", agentsAdminHandler.ListEnrollmentTokens)
	orgAgentsAdmin.POST("/agent-enrollment-tokens", agentsAdminHandler.MintEnrollmentToken)
	orgAgentsAdmin.DELETE("/agent-enrollment-tokens/:uid", agentsAdminHandler.DeleteEnrollmentToken)
	orgAgentsAdmin.GET("/agents", agentsAdminHandler.ListAgents)
	orgAgentsAdmin.DELETE("/agents/:uid", agentsAdminHandler.RevokeAgent)

	// Deported-agent WebSocket transport (spec 2026-07-16-02). Registered
	// directly on `api` — it carries its own auth, performed BEFORE the
	// upgrade: a one-shot spe_ enrollment bearer on first connect, Ed25519
	// signed headers on every reconnect. The claim/submit service logic is the
	// same code the historical HTTP worker API used; incidents always process
	// server-side.
	agentWorkersSvc := workers.NewService(
		s.dbService,
		s.services.CheckJobs,
		incidents.NewService(s.dbService, s.jobSvc, s.services.Clock, s.services.Realtime),
		scheduling.ParamsFromConfig(s.config.Server.Scheduling),
	)
	agentWSHandler := agentws.NewHandler(
		s.config,
		s.dbService,
		s.services.CheckJobs,
		agentWorkersSvc,
		s.services.Entitlements,
		s.services.EventNotifier,
		s.services.Credentials,
		agentsAdminSvc.ResealRegion,
	)
	api.GET("/agent/ws", agentWSHandler.Serve)

	// Results routes (authentication required)
	resultsService := results.NewService(s.dbService)
	resultsHandler := results.NewHandler(resultsService, s.config)
	orgResults := orgGroup("/orgs/:org/results")
	orgResults.GET("", resultsHandler.ListResults)

	// Per-check single result fetch (with fallback to covering aggregation)
	orgChecksResults := orgGroup("/orgs/:org/checks/:check/results")
	orgChecksResults.GET("/:uid", resultsHandler.GetResult)

	// Per-check availability statistics (real per-period probe-ratio + incidents)
	availabilityService := availability.NewService(s.dbService)
	availabilityHandler := availability.NewHandler(availabilityService, s.config)
	orgChecksAvail := orgGroup("/orgs/:org/checks/:check/availability")
	orgChecksAvail.GET("", availabilityHandler.GetAvailability)

	// Incidents routes (authentication required)
	incidentsService := incidents.NewService(s.dbService, s.jobSvc, s.services.Clock, s.services.Realtime)
	incidentsHandler := incidents.NewHandler(incidentsService, s.config)
	orgIncidents := orgGroup("/orgs/:org/incidents")
	orgIncidents.GET("", incidentsHandler.ListIncidents)
	orgIncidents.GET("/:uid", incidentsHandler.GetIncident)
	orgIncidents.POST("/:uid/ack", incidentsHandler.AcknowledgeIncident)
	orgIncidents.POST("/:uid/unack", incidentsHandler.UnacknowledgeIncident)
	orgIncidents.POST("/:uid/snooze", incidentsHandler.SnoozeIncident)
	orgIncidents.POST("/:uid/unsnooze", incidentsHandler.UnsnoozeIncident)
	orgIncidents.POST("/:uid/resolve", incidentsHandler.ResolveIncident)
	orgIncidents.POST("/:uid/comments", incidentsHandler.AddComment)

	// Magic-link ack — public route (the signed token authenticates).
	// Returns text/html so it renders in a browser opened from a mail client.
	publicOrgAPI.GET("/orgs/:org/incidents/:uid/ack", incidentsHandler.AcknowledgeIncidentByLink)

	// Per-recipient email unsubscribe (spec 2026-07-05-10, D4) — public,
	// top-level routes (not under /api/v1: these are browser pages and a
	// mail-client-submitted one-click POST, not JSON API calls). The signed
	// unsubscribe token authenticates; org is embedded in the token, not the
	// URL path, since a recipient's unsubscribe link has no other org
	// context to carry it in.
	unsubscribeService := unsubscribe.NewService(s.dbService, []byte(s.config.Auth.JWTSecret))
	unsubscribeHandler := unsubscribe.NewHandler(unsubscribeService, s.config)
	mainGroup.POST("/unsubscribe", unsubscribeHandler.OneClickUnsubscribe)
	mainGroup.GET("/unsubscribe", unsubscribeHandler.ConfirmationPage)
	mainGroup.GET("/unsubscribe/undo", unsubscribeHandler.Undo)

	// Incident notifications read API (authentication required)
	incidentNotifService := incidentnotifications.NewService(s.dbService)
	incidentNotifHandler := incidentnotifications.NewHandler(incidentNotifService, s.config)
	orgIncidents.GET("/:uid/notifications", incidentNotifHandler.ListForIncident)
	orgIncidents.GET("/:uid/notifications/:notifUid", incidentNotifHandler.GetForIncident)
	orgGroup("/orgs/:org/users").
		GET("/:uid/notifications", incidentNotifHandler.ListForUser)
	orgGroup("/orgs/:org/me").
		GET("/notifications", incidentNotifHandler.ListForMe)
	// Org-level notification endpoints (flat, no incident required)
	orgNotifs := orgGroup("/orgs/:org/notifications")
	orgNotifs.GET("", incidentNotifHandler.ListByOrg)
	orgNotifs.GET("/:notifUid", incidentNotifHandler.GetByOrg)

	// On-call schedules (authentication required)
	onCallService := oncallschedules.NewService(s.dbService)
	onCallHandler := oncallschedules.NewHandler(onCallService, s.dbService, s.config)
	orgOnCall := orgGroup("/orgs/:org/on-call-schedules")
	orgOnCall.GET("", onCallHandler.ListSchedules)
	orgOnCall.POST("", onCallHandler.CreateSchedule)
	orgOnCall.GET("/:uid", onCallHandler.GetSchedule)
	orgOnCall.PATCH("/:uid", onCallHandler.UpdateSchedule)
	orgOnCall.DELETE("/:uid", onCallHandler.DeleteSchedule)
	orgOnCall.GET("/:uid/preview", onCallHandler.PreviewSchedule)
	orgOnCall.GET("/:uid/overrides", onCallHandler.ListOverrides)
	orgOnCall.POST("/:uid/overrides", onCallHandler.CreateOverride)
	orgOnCall.DELETE("/:uid/overrides/:overrideUid", onCallHandler.DeleteOverride)
	orgOnCall.POST("/:uid/ical-feed/enable", onCallHandler.EnableICalFeed)
	orgOnCall.POST("/:uid/ical-feed/disable", onCallHandler.DisableICalFeed)
	orgOnCall.POST("/:uid/ical-feed/rotate", onCallHandler.RotateICalFeed)

	// Public iCal feed — the secret in the URL authorizes access. No auth
	// middleware: clients are calendar apps that can't bear tokens.
	api.GET("/on-call-schedules/:secret/feed.ics", onCallHandler.ServeICalFeed)

	// Wire the on-call resolver into the escalation step job so
	// schedule-target steps can find who is paged at fire time. The
	// indirection avoids an import cycle between jobtypes and
	// oncallschedules; jobtypes only knows the function shape.
	jobtypes.SetOnCallResolver(func(
		ctx context.Context, _ *jobdef.JobContext, scheduleUID string, at time.Time,
	) (*models.User, error) {
		return onCallService.Resolve(ctx, scheduleUID, at)
	})

	// Escalation policies (authentication required)
	escalationService := escalationpolicies.NewService(s.dbService)
	escalationHandler := escalationpolicies.NewHandler(escalationService, s.config)
	orgEscalation := orgGroup("/orgs/:org/escalation-policies")
	orgEscalation.GET("", escalationHandler.ListPolicies)
	orgEscalation.POST("", escalationHandler.CreatePolicy)
	orgEscalation.GET("/:uid", escalationHandler.GetPolicy)
	orgEscalation.PATCH("/:uid", escalationHandler.UpdatePolicy)
	orgEscalation.DELETE("/:uid", escalationHandler.DeletePolicy)

	// User notification routes (authentication required)
	userNotifService := usernotifications.NewService(
		s.dbService, s.services.Credentials,
		// Instance-level WhatsApp credentials power the contact-verification
		// authentication template. Off by default; NewService keeps the feature
		// dark when the config is inactive.
		usernotifications.WithWhatsAppConfig(&s.config.WhatsApp),
		// Instance-level Telegram credentials power the connect-link flow. Off
		// by default; CreateTelegramLink refuses while the config is inactive.
		usernotifications.WithTelegramConfig(&s.config.Telegram),
	)
	emailAdapter := usernotifications.NewEmailSenderAdapter(s.services.EmailSender, s.services.EmailFormatter)
	slackAdapter := usernotifications.NewSlackDMSenderAdapter()
	userNotifHandler := usernotifications.NewHandler(
		userNotifService, s.config, emailAdapter, slackAdapter, s.services.WebPushOptions,
	)
	orgUserNotif := orgGroup("/orgs/:org/users/me")
	orgUserNotif.GET("/notification-routes", userNotifHandler.ListRoutes)
	orgUserNotif.POST("/notification-contacts", userNotifHandler.CreateContact)
	orgUserNotif.PATCH("/notification-routes/:routeUid", userNotifHandler.PatchRoute)
	orgUserNotif.DELETE("/notification-contacts/:contactUid", userNotifHandler.DeleteContact)
	orgUserNotif.POST("/notification-routes/:routeUid/test", userNotifHandler.TestRoute)
	orgUserNotif.POST("/notification-contacts/:contactUid/verify", userNotifHandler.VerifyContact)
	orgUserNotif.POST("/notification-contacts/:contactUid/verify/confirm", userNotifHandler.ConfirmVerify)
	// Telegram connect link. Always registered (it answers a clean
	// VALIDATION_ERROR when the instance has no bot) so the dashboard gets a
	// meaningful message rather than a 404 it would have to guess about.
	orgUserNotif.POST("/telegram/link", userNotifHandler.CreateTelegramLink)

	// Events routes (authentication required)
	eventsService := events.NewService(s.dbService)
	eventsHandler := events.NewHandler(eventsService, s.config)
	orgEvents := orgGroup("/orgs/:org/events")
	orgEvents.GET("", eventsHandler.ListEvents)

	// Realtime hint WebSocket. The hub/handler are always constructed and the
	// route is always registered — even when SP_REALTIME_ENABLED=false — so a
	// disabled feature still accepts the upgrade and immediately closes with
	// 4404 (browsers cannot see HTTP status at upgrade time, only close
	// codes; see handlers/realtimews). Registered OUTSIDE
	// RequireAuth/RequireOrgAccess: browsers cannot present credentials at
	// WebSocket-upgrade time, so the handler performs the exact same
	// validation in-band and closes 4401/4403 otherwise. The path is
	// excluded from the request timeout and rate limits
	// (middleware.isExcluded) — the hub's max_connections and
	// max_subscriptions_per_connection guards bound it instead.
	s.realtimeHub = realtime.NewHubWithSubscriptionCap(
		s.services.EventNotifier, s.config.Realtime.MaxConnections,
		s.config.Realtime.MaxSubscriptionsPerConnection, slog.Default())
	wsHandler := realtimews.NewHandler(s.realtimeHub, s.authService, s.dbService, s.config)
	api.GET("/orgs/:org/events/ws", wsHandler.Serve)

	// Files routes (authentication required for org-scoped, plus public signed-URL route)
	filesService := files.NewService(s.dbService, s.config)
	filesHandler := files.NewHandler(filesService, s.config)
	orgFiles := orgGroup("/orgs/:org/files")
	orgFiles.GET("", filesHandler.List)
	orgFiles.GET("/:uid", filesHandler.Get)
	orgFiles.GET("/:uid/content", filesHandler.GetContent)
	orgFiles.DELETE("/:uid", filesHandler.Delete)
	pubFiles := mainGroup.NewGroup("/pub/files")
	pubFiles.GET("/:uid", filesHandler.PublicGet)

	// Organization logo (spec 2026-08-08-12). Upload/clear are owner-gated; the
	// public route is unsigned on purpose — a logo URL has to be stable enough
	// to paste into a status page — and is authorized by state instead: the
	// file must be the CURRENT logo of a live organization.
	orgLogoService := orglogo.NewService(s.dbService, filesService)
	orgLogoHandler := orglogo.NewHandler(orgLogoService, s.config)
	orgOwnerGroup.POST("/logo", orgLogoHandler.Upload)
	orgOwnerGroup.DELETE("/logo", orgLogoHandler.Delete)
	pubOrgLogos := mainGroup.NewGroup("/pub/org-logos")
	pubOrgLogos.GET("/:uid", orgLogoHandler.PublicGet)

	// Bug report (public POST under /api/mgmt) and features endpoint (auth)
	feedbackService := feedback.NewService(s.dbService, filesService, s.config, nil)
	feedbackHandler := feedback.NewHandler(feedbackService, s.authService, s.config)
	featuresHandler := features.NewHandler(s.config)
	api.NewGroup("/features").Use(authMiddleware.RequireAuth).GET("", featuresHandler.GetFeatures)

	// Members routes (authentication required).
	//
	// Reads stay open to any member of the org — the escalation-policy editor
	// and the member picker read this list as a plain user.
	//
	// Writes are admin-only (spec 2026-08-09-03). `members.Service`
	// additionally requires *owner* to touch an owner or to grant ownership
	// (spec 2026-08-08-11); this gate is the floor beneath that, not a
	// replacement for it.
	membersService := members.NewService(s.dbService,
		// Powers the admin "set up your paging" nudge email (spec 2026-08-12-03).
		members.WithEmailSender(s.services.EmailSender),
		members.WithEmailFormatter(s.services.EmailFormatter),
		members.WithAppBaseURL(s.config.Server.BaseURL))
	membersHandler := members.NewHandler(membersService, s.config)
	orgMembers := orgGroup("/orgs/:org/members")
	orgMembers.GET("", membersHandler.ListMembers)
	orgMembers.GET("/:uid", membersHandler.GetMember)

	orgMembersAdmin := api.NewGroup("/orgs/:org/members").
		Use(orgSlugRedirect.Middleware,
			authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
	// Paging coverage (spec 2026-08-12-03). Admin-only and type-level only:
	// per-user routes are otherwise `users/me`-scoped, and this view exposes
	// channel types + verified/enabled flags, never a phone number or chat id.
	orgMembersAdmin.GET("/coverage", membersHandler.ListCoverage)
	orgMembersAdmin.POST("", membersHandler.AddMember)
	orgMembersAdmin.PATCH("/:uid", membersHandler.UpdateMember)
	orgMembersAdmin.DELETE("/:uid", membersHandler.RemoveMember)
	// Admin pre-provisioning (spec 2026-08-12-03, phase 3). An admin may add a
	// phone/WhatsApp contact for a colleague, but ONLY unverified: it becomes
	// pageable when its owner completes the normal verification round-trip. The
	// nudge simply asks them to do that.
	orgMembersAdmin.POST("/:uid/contacts", membersHandler.AddMemberContact)
	orgMembersAdmin.POST("/:uid/paging-nudge", membersHandler.SendPagingNudge)

	// System parameters routes (super admin only)
	systemService := system.NewService(s.dbService)
	systemService.SetEmailFormatter(s.services.EmailFormatter)

	// JMAP inbox manager: long-running supervisor that connects to the
	// configured JMAP server and dispatches incoming emails to handlers.
	// The supervisor is started from Server.Start() once we have a real
	// cancellable context.
	s.jmapManager = jmap.NewManager(s.dbService)
	s.jmapManager.RegisterHandler(emailcheck.NewHandler(s.dbService, s.jobSvc, s.services.Realtime, slog.Default()))
	systemService.SetEmailInboxManager(s.jmapManager)

	systemHandler := system.NewHandler(systemService, s.config)
	systemGroup := api.NewGroup("/system/parameters").
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireSuperAdmin)
	systemGroup.GET("", systemHandler.ListParameters)
	systemGroup.GET("/:key", systemHandler.GetParameter)
	systemGroup.PUT("/:key", systemHandler.SetParameter)
	systemGroup.DELETE("/:key", systemHandler.DeleteParameter)

	// Public projection of email_inbox: any authenticated user can read
	// addressDomain so per-check email addresses can be rendered without
	// surfacing the rest of the JMAP credentials.
	api.NewGroup("/system/parameters").
		Use(authMiddleware.RequireAuth).
		GET("/email_inbox/public", systemHandler.EmailInboxPublic)

	// System actions routes (super admin only)
	systemActions := api.NewGroup("/system").
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireSuperAdmin)
	systemActions.POST("/test-email", systemHandler.TestEmail)
	systemActions.GET("/email-inbox/config", systemHandler.EmailInboxConfig)
	systemActions.GET("/email-inbox/status", systemHandler.EmailInboxStatus)
	systemActions.POST("/email-inbox/test", systemHandler.EmailInboxTest)
	systemActions.POST("/email-inbox/sync", systemHandler.EmailInboxSync)
	systemActions.GET("/activation", systemHandler.ListActivationFunnel)
	systemActions.GET("/scheduling/lane-load", systemHandler.LaneLoad)
	// Fleet-wide agent view (spec 2026-08-05-01): org agents are already
	// listed per-org, but system agents (kind='system', no owning org) are
	// otherwise visible nowhere short of querying the DB by hand.
	systemActions.GET("/agents", agentsAdminHandler.ListAllAgents)

	// Org entitlements routes. The handler does its own auth gating
	// (trusted service preferred for the SaaS billing service; admin user
	// fallback gated by entitlements.admin_writes_enabled).
	//
	// The billing service proves identity by SIGNING the request
	// (X-SP-Signature over <timestamp>.<method>.<path>.<sha256 body>, keys in
	// entitlements.service_signing_keys) — ServiceSignature verifies it.
	// ServiceTokenBypass stays behind it for the legacy static bearer, gated
	// by entitlements.allow_legacy_service_token (default true) so the
	// cross-repo migration is a parameter flip rather than a lockstep restart.
	// Either one marks the request as a trusted service, so the following
	// RequireAuth + RequireOrgAccess become no-ops (cross-org writes); every
	// other caller authenticates normally.
	entitlementsHandler := entitlements.NewHandler(s.services.Entitlements, s.dbService, s.config)
	orgEntitlements := api.NewGroup("/orgs/:org/entitlements").
		Use(orgSlugRedirect.Middleware).
		Use(authMiddleware.ServiceSignature(entitlements.ParamServiceSigningKeys)).
		Use(authMiddleware.ServiceTokenBypass(
			entitlements.ParamServiceToken, entitlements.ParamAllowLegacyServiceToken)).
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireOrgAccess)
	orgEntitlements.GET("", entitlementsHandler.Get)
	orgEntitlements.PUT("", entitlementsHandler.Put)
	orgEntitlements.PATCH("", entitlementsHandler.Patch)
	orgEntitlements.GET("/audits", entitlementsHandler.ListAudits)

	// Web Push routes (authentication required).
	webpushHandler := webpushhandler.NewHandler(s.config)
	orgWebPush := orgGroup("/orgs/:org/webpush")
	orgWebPush.GET("/vapid-public-key", webpushHandler.GetVAPIDPublicKey)

	// Integration routes (authentication required).
	//
	// `/integrations` is canonical (the umbrella entity operators see in the
	// UI); `/channels` is kept as a one-cycle alias (the prior name — "channel"
	// now means the notify role). The original legacy `/connections` path was
	// dropped in PR-E; a follow-up drops `/channels`.
	integrationsService := integrations.NewService(
		s.dbService, s.services.Credentials, s.services, s.config)
	integrationsHandler := integrations.NewHandler(integrationsService, s.config)
	for _, prefix := range []string{
		"/orgs/:org/integrations",
		"/orgs/:org/channels",
	} {
		group := orgGroup(prefix)
		group.GET("", integrationsHandler.ListIntegrations)
		group.POST("", integrationsHandler.CreateIntegration)
		group.GET("/:uid", integrationsHandler.GetIntegration)
		group.PATCH("/:uid", integrationsHandler.UpdateIntegration)
		group.DELETE("/:uid", integrationsHandler.DeleteIntegration)
		// Standard Webhooks: rotate the per-integration signing secret
		// (webhook-only, 400 otherwise).
		group.POST("/:uid/rotate-secret", integrationsHandler.RotateWebhookSecret)
		// Send a sample notification through any notifiable integration to
		// verify it's wired correctly (400 for data-source-only types).
		group.POST("/:uid/test", integrationsHandler.TestIntegration)
	}

	// Per-integration member identity mapping (spec 2026-08-12-03) — "who is
	// this org member on this Slack workspace", used to mention the on-call
	// person in channel alerts. Reading the mapping is open to any member (the
	// Slack panel renders it); every write is admin-only, because a wrong
	// mapping makes an alert ping the wrong human.
	orgIntegrationIdentities := orgGroup("/orgs/:org/integrations/:uid/identities")
	orgIntegrationIdentities.GET("", integrationsHandler.ListIdentities)

	orgIntegrationIdentitiesAdmin := api.NewGroup("/orgs/:org/integrations/:uid/identities").
		Use(orgSlugRedirect.Middleware,
			authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
	orgIntegrationIdentitiesAdmin.POST("/sync", integrationsHandler.SyncIdentities)
	orgIntegrationIdentitiesAdmin.PUT("/:userUid", integrationsHandler.SetIdentity)
	orgIntegrationIdentitiesAdmin.DELETE("/:userUid", integrationsHandler.DeleteIdentity)

	// Freebox pairing endpoints — separate from the generic CRUD because
	// they wrap the multi-step LCD-approval handshake. POST creates the
	// integration in `pairing` status and asks the Freebox for an
	// app_token; GET polls until the user approves the prompt.
	orgFreebox := orgGroup("/orgs/:org/integrations/freebox")
	orgFreebox.POST("/pair", integrationsHandler.StartFreeboxPairing)
	orgFreebox.GET("/pair/:uid/status", integrationsHandler.GetFreeboxPairingStatus)
	// LAN discovery: returns the list of hosts currently visible to the
	// Freebox so the dashboard can pre-fill ICMP checks without typing.
	// Requires a `granted` integration — see Service.ListFreeboxLanHosts.
	orgFreebox.GET("/:uid/lan-hosts", integrationsHandler.LanHostsHandler)

	// Email unsubscribe list (spec 2026-07-05-10, D4). Read+delete only —
	// creation is always recipient-initiated via the public
	// handlers/unsubscribe surface, never through this authenticated API.
	emailSuppressionsService := emailsuppressions.NewService(s.dbService)
	emailSuppressionsHandler := emailsuppressions.NewHandler(emailSuppressionsService, s.config)
	orgEmailSuppressions := orgGroup("/orgs/:org/email-suppressions")
	orgEmailSuppressions.GET("", emailSuppressionsHandler.ListSuppressions)
	orgEmailSuppressions.DELETE("/:uid", emailSuppressionsHandler.DeleteSuppression)

	// Status updates routes (authentication required)
	statusUpdatesService := statusupdates.NewService(s.dbService)
	// Fan published status updates out to confirmed status-page subscribers by
	// email. The notifier runs detached (fire-and-forget) inside the service.
	statusSubscriberNotifier := statussubscribers.NewNotifier(
		s.dbService, s.services.EmailSender, s.services.EmailFormatter, s.config.Server.BaseURL, slog.Default())
	statusUpdatesService.SetSubscriberNotifier(statusSubscriberNotifier)
	statusUpdatesHandler := statusupdates.NewHandler(statusUpdatesService, s.config)
	orgStatusUpdates := orgGroup("/orgs/:org/status-updates")
	orgStatusUpdates.GET("", statusUpdatesHandler.ListStatusUpdates)
	orgStatusUpdates.POST("", statusUpdatesHandler.CreateStatusUpdate)
	orgStatusUpdates.GET("/:uid", statusUpdatesHandler.GetStatusUpdate)
	orgStatusUpdates.PATCH("/:uid", statusUpdatesHandler.UpdateStatusUpdate)
	orgStatusUpdates.DELETE("/:uid", statusUpdatesHandler.DeleteStatusUpdate)

	// Status pages routes (authentication required)
	statusPagesService := statuspages.NewService(s.dbService, s.config, s.services.Entitlements)
	// Retained on the server so serveStatus0Static can resolve pages for
	// per-page Open Graph / Twitter Card metadata injection.
	s.statusPagesService = statusPagesService
	statusPagesHandler := statuspages.NewHandler(statusPagesService, s.config)
	orgStatusPages := orgGroup("/orgs/:org/status-pages")
	orgStatusPages.GET("", statusPagesHandler.ListStatusPages)
	orgStatusPages.POST("", statusPagesHandler.CreateStatusPage)
	orgStatusPages.GET("/:statusPageUid", statusPagesHandler.GetStatusPage)
	orgStatusPages.PATCH("/:statusPageUid", statusPagesHandler.UpdateStatusPage)
	orgStatusPages.DELETE("/:statusPageUid", statusPagesHandler.DeleteStatusPage)
	// Custom-domain verify-now (authenticated): runs the DNS checks synchronously.
	orgStatusPages.POST("/:statusPageUid/custom-domain/verify", statusPagesHandler.VerifyCustomDomain)
	orgStatusPages.GET("/:statusPageUid/sections", statusPagesHandler.ListSections)
	orgStatusPages.POST("/:statusPageUid/sections", statusPagesHandler.CreateSection)
	orgStatusPages.POST("/:statusPageUid/sections/reorder", statusPagesHandler.ReorderSections)
	orgStatusPages.GET("/:statusPageUid/sections/:sectionUid", statusPagesHandler.GetSection)
	orgStatusPages.PATCH("/:statusPageUid/sections/:sectionUid", statusPagesHandler.UpdateSection)
	orgStatusPages.DELETE("/:statusPageUid/sections/:sectionUid", statusPagesHandler.DeleteSection)
	orgStatusPages.GET("/:statusPageUid/sections/:sectionUid/resources", statusPagesHandler.ListResources)
	orgStatusPages.POST("/:statusPageUid/sections/:sectionUid/resources", statusPagesHandler.CreateResource)
	orgStatusPages.POST(
		"/:statusPageUid/sections/:sectionUid/resources/reorder", statusPagesHandler.ReorderResources)
	orgStatusPages.PATCH("/:statusPageUid/sections/:sectionUid/resources/:resourceUid", statusPagesHandler.UpdateResource)
	orgStatusPages.DELETE("/:statusPageUid/sections/:sectionUid/resources/:resourceUid", statusPagesHandler.DeleteResource)

	// Status page subscribers (public email/RSS subscriptions). The handler is
	// shared by the authed admin routes (below) and the public routes (further
	// down, outside RequireAuth).
	statusSubscribersService := statussubscribers.NewService(s.dbService)
	statusSubscribersHandler := statussubscribers.NewHandler(
		statusSubscribersService, s.dbService, s.services.EmailSender, s.services.EmailFormatter, s.config)
	// Authed admin: list (count + redactable addresses) and remove.
	orgStatusPages.GET("/:statusPageUid/subscribers", statusSubscribersHandler.ListSubscribers)
	orgStatusPages.DELETE("/:statusPageUid/subscribers/:uid", statusSubscribersHandler.RemoveSubscriber)

	// Maintenance windows routes (authentication required)
	mwService := maintenancewindows.NewService(s.dbService)
	mwHandler := maintenancewindows.NewHandler(mwService, s.config)
	orgMW := orgGroup("/orgs/:org/maintenance-windows")
	orgMW.GET("", mwHandler.List)
	orgMW.POST("", mwHandler.Create)
	orgMW.GET("/:uid", mwHandler.Get)
	orgMW.PATCH("/:uid", mwHandler.Update)
	orgMW.DELETE("/:uid", mwHandler.Delete)
	orgMW.GET("/:uid/checks", mwHandler.ListChecks)
	orgMW.PUT("/:uid/checks", mwHandler.SetChecks)

	// Public status page endpoints (no authentication)
	publicOrgAPI.GET("/status-pages/:org", statusPagesHandler.ViewDefaultStatusPage)
	publicOrgAPI.GET("/status-pages/:org/:slug", statusPagesHandler.ViewStatusPage)
	// Lightweight "is it up?" rollup — cheap alternative to the full page view
	// above, with a Cache-Control header the full view doesn't set (spec
	// 2026-08-08-06).
	publicOrgAPI.GET("/status-pages/:org/:slug/summary", statusPagesHandler.ViewStatusPageSummary)
	// Public SVG badge (page-level rollup) — same sibling as the per-check
	// badge under /orgs/:org/checks/:check/badges/:components (spec
	// 2026-08-08-07).
	publicOrgAPI.GET("/status-pages/:org/:slug/badge", statusPagesHandler.GetBadge)
	// Public Atom/RSS feed of the status-update timeline.
	publicOrgAPI.GET("/status-pages/:org/:slug/feed.xml", statusSubscribersHandler.Feed)

	// Edge-TLS "ask" endpoint (public, no auth): 204 when the queried domain is a
	// verified+enabled+public custom domain, 404 otherwise. Contract for Caddy
	// on_demand_tls / cert-manager gating on the SaaS edge.
	api.GET("/public/custom-domains/allowed", statusPagesHandler.CustomDomainAllowed)

	// Public status-page subscription endpoints (no authentication). The
	// subscribe endpoint inherits the global per-IP rate limit on /api/v1/;
	// double opt-in is the primary anti-abuse control. Confirm/unsubscribe are
	// single-purpose token links that render an HTML landing page.
	publicOrgAPI.POST("/orgs/:org/status-pages/:statusPageUid/subscribers", statusSubscribersHandler.Subscribe)
	publicSubscribers := api.NewGroup("/public/status-subscribers")
	publicSubscribers.GET("/confirm", statusSubscribersHandler.Confirm)
	publicSubscribers.GET("/unsubscribe", statusSubscribersHandler.Unsubscribe)

	// Slack integration routes (inbound from Slack - no org auth)
	slackService := slack.NewService(s.dbService, s.config, s.authService, checksService, incidentsService)
	// Auto-match members to workspace users right after an install so the first
	// alert can already mention the on-call person (spec 2026-08-12-03). The
	// dependency can only point this way — handlers/integrations imports the
	// slack package — hence the function seam.
	slackService.SetIdentitySync(func(ctx context.Context, orgSlug, integrationUID string) error {
		_, err := integrationsService.SyncIdentities(ctx, orgSlug, integrationUID)

		return err
	})
	slackHandler := slack.NewHandler(slackService, s.config)

	// Build the Socket Mode supervisor up-front when enabled so its status is
	// readable via GET /integrations/slack/socket/status even before Start().
	// The actual Run() goroutine is launched from Start() under workersWg.
	if s.config.Slack.Enabled && s.config.Slack.SocketModeEnabled && s.config.ShouldRunAPI() {
		s.slackSocketSupervisor = slack.NewSlackSocketSupervisor(slackService, s.config, slog.Default())
		slackHandler.SetSocketSupervisor(s.slackSocketSupervisor)
	}

	slackIntegration := api.NewGroup("/integrations/slack")
	slackIntegration.GET("/install", slackHandler.Install)
	slackIntegration.GET("/oauth", slackHandler.OAuthCallback)
	slackIntegration.GET("/socket/status", slackHandler.GetSocketStatus)
	// Apply signature verification middleware to Slack webhooks
	slackIntegration.POST("/events", slackHandler.VerifyMiddleware(slackHandler.HandleEvents))
	slackIntegration.POST("/command", slackHandler.VerifyMiddleware(slackHandler.HandleCommand))
	slackIntegration.POST("/interaction", slackHandler.VerifyMiddleware(slackHandler.HandleInteraction))

	// Microsoft Teams bot routes (inbound from Bot Framework — no org auth;
	// authenticity is the per-request JWT check against Microsoft's JWKS in
	// HandleMessages). The routes are always registered so the status and
	// manifest endpoints can explain an unconfigured instance; the messaging
	// endpoint itself refuses traffic with 503 while msteams.enabled is false.
	msTeamsService := msteams.NewService(s.dbService, s.config, checksService)
	msTeamsHandler := msteams.NewHandler(msTeamsService, s.config)

	// The messaging endpoint is the ONLY public route here — it has to be,
	// Microsoft calls it, and authenticity comes from the Bot Framework JWT
	// rather than session auth. Status, the app package and the link-code
	// endpoint all live on the authenticated org-scoped group further down:
	// served anonymously they would hand any caller the instance's Entra app
	// id, the pinned tenant GUID, and a live count of installed Microsoft 365
	// tenants.
	msTeamsIntegration := api.NewGroup("/integrations/msteams")
	msTeamsIntegration.POST("/messages", msTeamsHandler.HandleMessages)

	// Twilio inbound callbacks (voice TwiML + DTMF ack + delivery status).
	// No org auth — authenticity is the per-request X-Twilio-Signature check in
	// VerifyMiddleware, which resolves the connection from the `cid` query param.
	twilioHandler := twiliocb.NewHandler(s.dbService, s.services.Credentials, s.config, incidentsService)
	twilioIntegration := api.NewGroup("/integrations/twilio")
	twilioIntegration.POST("/voice", twilioHandler.VerifyMiddleware(twilioHandler.HandleVoice))
	twilioIntegration.POST("/voice/gather", twilioHandler.VerifyMiddleware(twilioHandler.HandleGather))
	twilioIntegration.POST("/status", twilioHandler.VerifyMiddleware(twilioHandler.HandleStatus))

	// Meta WhatsApp Cloud API inbound webhook (subscription handshake +
	// delivery statuses + inbound replies). Instance-level, so unlike Twilio
	// there is no org-scoped `cid`: the GET handshake matches the configured
	// verify token and the POST validates X-Hub-Signature-256 over the raw body
	// with the app secret before parsing. Registered only when the instance has
	// WhatsApp configured — an unconfigured deployment exposes no route at all.
	if s.config.WhatsApp.Active() {
		whatsAppHandler := whatsappcb.NewHandler(s.dbService, s.config)
		whatsAppIntegration := api.NewGroup("/integrations/whatsapp")
		whatsAppIntegration.GET("/webhook", whatsAppHandler.HandleVerify)
		whatsAppIntegration.POST("/webhook", whatsAppHandler.HandleEvent)
	}

	// Telegram Bot API inbound webhook (connect flow, in-chat opt-out, block
	// notifications). Instance-level like WhatsApp, but Telegram does NOT sign
	// its payloads: the only authenticity gate is the
	// X-Telegram-Bot-Api-Secret-Token header, compared constant-time before the
	// body is parsed. Registered only when the instance has Telegram configured
	// — an unconfigured deployment exposes no route at all.
	//
	// Gated on Configured(), NOT Active(): the bot @username is derived at boot
	// and may be unknown on a first run holding only a token. A route that was
	// never registered cannot be repaired without a restart, whereas an
	// early-registered one is 403-only until the secret is known.
	if s.config.Telegram.Configured() {
		telegramHandler := telegramcb.NewHandler(s.dbService, s.config,
			telegramcb.WithAcknowledger(incidentsService))
		telegramIntegration := api.NewGroup("/integrations/telegram")
		// Path kept in sync with TelegramWebhookPath, which the boot-time
		// self-registration uses.
		telegramIntegration.POST("/webhook", telegramHandler.HandleUpdate)
	}

	// Slack destinations picker (authenticated, org-scoped)
	slackOrgRoutes := orgGroup("/orgs/:org/channels/:uid/slack")
	slackOrgRoutes.GET("/destinations", slackHandler.GetDestinations)

	// Microsoft Teams destinations picker (authenticated, org-scoped). Unlike
	// Slack this reads the conversation references captured at install rather
	// than calling out to the provider — a Teams bot cannot enumerate the
	// channels of a team it was never added to.
	msTeamsOrgRoutes := orgGroup("/orgs/:org/channels/:uid/msteams")
	msTeamsOrgRoutes.GET("/destinations", msTeamsHandler.GetDestinations)

	// Teams bot setup (authenticated, org-scoped). link-code is the
	// proof-of-possession entry point: the org comes from the verified route
	// context, and the code it mints is the only thing that can bind a
	// Microsoft 365 tenant to this org.
	msTeamsOrgIntegrationRoutes := api.NewGroup("/orgs/:org/integrations/msteams").
		Use(orgSlugRedirect.Middleware, authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
	msTeamsOrgIntegrationRoutes.POST("/link-code", msTeamsHandler.StartLink)
	msTeamsOrgIntegrationRoutes.GET("/status", msTeamsHandler.GetStatus)
	msTeamsOrgIntegrationRoutes.GET("/manifest.zip", msTeamsHandler.DownloadManifest)

	// Org-scoped install-URL minting (spec 2026-07-05-01): the org comes from
	// the authenticated route context (RequireOrgAccess), never from a query
	// param, so a workspace already connected to another org can be installed
	// again here without landing the user in — or joining them to — that
	// other org.
	slackOrgIntegrationRoutes := api.NewGroup("/orgs/:org/integrations/slack").
		Use(orgSlugRedirect.Middleware, authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
	slackOrgIntegrationRoutes.POST("/install-url", slackHandler.BuildInstallURLForOrg)

	// Incident events (authentication required)
	orgIncidents.GET("/:uid/events", eventsHandler.ListIncidentEvents)

	// Check events (authentication required)
	orgChecks.GET("/:checkUid/events", eventsHandler.ListCheckEvents)

	// Management endpoints
	mgmt := mainGroup.NewGroup("/api/mgmt")
	mgmt.GET("/health", s.healthCheck)
	mgmt.GET("/version", s.getVersion)
	mgmt.GET("/limits", s.getLimits)
	mgmt.POST("/report", feedbackHandler.SubmitReport)

	// Memory snapshot (super-admin only): runtime memstats, process RSS,
	// suspect-subsystem sizes and build cgo/SQLite-driver facts. Gated because
	// memstats + subsystem cardinality are operationally sensitive, unlike
	// health/version. The raw pprof surface stays on the localhost-bound
	// profiler server.
	mgmtAdmin := mainGroup.NewGroup("/api/mgmt").
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireSuperAdmin)
	mgmtAdmin.GET("/memory", s.getMemory)
	// Scheduler cost/delay distribution (super-admin, read-only): aggregate
	// percentiles of cost_ewma_ms / delay_ewma_ms across check_jobs plus
	// fast/slow counts at a candidate threshold. Low-cardinality analysis for
	// the fast/slow-lane go/no-go decision (spec 2026-07-01-01).
	mgmtAdmin.GET("/scheduling/cost-distribution", s.getCostDistribution)

	// Prometheus metrics endpoint
	if s.config.Prometheus.Enabled {
		prommetrics.Register(prometheus.DefaultRegisterer)
		s.registerSubsystemMetrics(prometheus.DefaultRegisterer)

		metricsPath := s.config.Prometheus.Path
		if metricsPath == "" {
			metricsPath = "/metrics"
		}

		mainGroup.GET(metricsPath, httpx.HTTPHandler(promhttp.Handler()))

		slog.InfoContext(ctx, "Prometheus metrics endpoint enabled", "path", metricsPath)
	}

	// Test API routes (no authentication for development/testing)
	testHandler := testapi.NewHandler(s.jobSvc, s.dbService, s.services.EventNotifier)
	api.POST("/test/jobs", testHandler.CreateEmailJob)
	api.GET("/fake", testHandler.FakeAPI)

	if s.config.RunMode == "test" {
		api.GET("/test/state-entries", testHandler.ListStateEntries)
		api.POST("/test/users", testHandler.CreateUser)
		api.POST("/test/checks/bulk", testHandler.BulkCreateChecks)
		api.DELETE("/test/checks/bulk", testHandler.BulkDeleteChecks)
		api.POST("/test/generate-data", testHandler.GenerateData)
		api.DELETE("/test/checks/all", testHandler.DeleteAllChecks)

		// Email design preview (spec D5): renders any shipped template with
		// fixture data through the real formatter, so the design can be
		// iterated on in a browser without round-tripping through SMTP.
		// Not compiled out — just 404s outside test mode, like the routes
		// above.
		emailPreviewHandler := emailpreview.NewHandler(s.services.EmailFormatter, s.config)
		mgmt.GET("/email-preview/:template", emailPreviewHandler.Preview)
	}

	// OpenAPI schema + interactive (Swagger) explorer. The explorer moved from
	// /docs to /openapi now that /docs serves the documentation site.
	mainGroup.GET("/openapi.yaml", s.serveFile(openAPIFiles, "openapi/openapi.yaml"))
	mainGroup.GET("/openapi", s.serveFile(openAPIFiles, "openapi/index.html"))

	// llms.txt / llms-full.txt at the conventional root path (GitHub issue
	// #183), generated by docusaurus-plugin-llms alongside the rest of the
	// embedded docs build. Reuses serveDocsFile so a missing file 404s (via
	// the docs 404 page) instead of falling through to the SPA catch-all.
	mainGroup.GET("/llms.txt", s.serveRootLLMsTxt)
	mainGroup.GET("/llms-full.txt", s.serveRootLLMsFullTxt)

	// Documentation site (Docusaurus), embedded and served at /docs on every
	// host. docs.solidping.io redirects its root here (see handlerWithDocsHost).
	mainGroup.GET("/docs", s.serveDocsRoute)
	mainGroup.GET("/docs/*path", s.serveDocsRoute)

	// Dash0 status page (served at /dash0/)
	mainGroup.GET("/dash0", s.serveDash0Root)
	mainGroup.GET("/dash0/*path", s.serveDash0Root)

	// Embeddable status widget (spec 2026-08-08-08). Frozen public contract:
	// customers paste this URL into their own sites, so it must keep working
	// forever — new behavior goes to /embed/v2/widget.js, never here.
	mainGroup.GET("/embed/v1/widget.js", s.serveEmbedWidgetV1)

	// Status0 public status page (served at /status0/)
	mainGroup.GET("/status0", s.serveStatus0Root)
	mainGroup.GET("/status0/*path", s.serveStatus0Root)

	// Catch-all for frontend (must be last)
	mainGroup.GET("/*path", s.serveAppRoot)

	s.router = router
}

// initSentry initializes the Sentry SDK for error tracking.
// If no DSN is configured, Sentry is silently disabled.
func initSentry(cfg config.SentryConfig) error {
	if cfg.DSN == "" {
		slog.Info("Sentry disabled (no DSN configured)")
		return nil
	}

	err := sentry.Init(sentry.ClientOptions{
		Dsn:              cfg.DSN,
		Environment:      cfg.Environment,
		Release:          "solidping-server@" + version.Version,
		TracesSampleRate: cfg.TracesSampleRate,
		Debug:            cfg.Debug,
		BeforeSend: func(event *sentry.Event, _ *sentry.EventHint) *sentry.Event {
			if event.Request == nil {
				return event
			}
			// Scrub sensitive headers
			for key := range event.Request.Headers {
				if key == "Authorization" || key == "Cookie" {
					event.Request.Headers[key] = "[FILTERED]"
				}
			}
			return event
		},
	})
	if err != nil {
		return fmt.Errorf("sentry init: %w", err)
	}

	slog.Info("Sentry initialized", "environment", cfg.Environment)

	return nil
}

func (s *Server) corsMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(writer http.ResponseWriter, req *http.Request) error {
		writer.Header().Set("Access-Control-Allow-Origin", "*")
		writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS, PATCH")
		writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, Accept, Origin")
		writer.Header().Set("Access-Control-Max-Age", "86400")
		writer.Header().Set("Access-Control-Allow-Credentials", "true")

		if req.Method == http.MethodOptions {
			writer.WriteHeader(http.StatusOK)
			return nil
		}

		return next(writer, req)
	}
}

func (s *Server) loggingMiddleware(next httpx.HandlerFunc) httpx.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) error {
		start := time.Now()
		err := next(w, req)
		duration := time.Since(start)

		slog.InfoContext(req.Context(), "HTTP request",
			"method", req.Method,
			"path", req.URL.Path,
			"duration", duration,
			"error", err,
		)

		return err
	}
}

// HealthResponse represents the health check response.
type HealthResponse struct {
	Status string          `json:"status"`
	Node   *HealthNodeInfo `json:"node,omitempty"`
	// Fault names the structural database fault that made this node
	// unhealthy ("undefined_table", …). Empty on a healthy node.
	Fault string `json:"fault,omitempty"`
}

// HealthNodeInfo contains node information for health response.
type HealthNodeInfo struct {
	Role   string `json:"role"`
	Region string `json:"region,omitempty"`
}

func (s *Server) healthCheck(writer http.ResponseWriter, _ *http.Request) error {
	writer.Header().Set("Content-Type", "application/json")

	response := HealthResponse{
		Status: "ok",
		Node: &HealthNodeInfo{
			Role: s.config.Node.Role,
		},
	}

	// A process whose schema has vanished answers every query with an error; it
	// must not answer "ok" here while it does. It is on its way out, but the
	// shutdown window is long enough for an orchestrator to see this.
	status := http.StatusOK
	if fault := s.dbFault.Fault(); fault != nil {
		status = http.StatusServiceUnavailable
		response.Status = "unhealthy"
		response.Fault = fault.Reason
	}

	writer.WriteHeader(status)

	if s.config.Node.Region != "" {
		response.Node.Region = s.config.Node.Region
	}

	data, err := json.Marshal(response)
	if err != nil {
		return err
	}

	_, err = writer.Write(data)

	return err
}

// LimitsRateLimit is the rate-limit section of the /api/mgmt/limits response.
type LimitsRateLimit struct {
	Enabled           bool     `json:"enabled"`
	RequestsPerMinute int      `json:"requestsPerMinute,omitempty"`
	Burst             int      `json:"burst,omitempty"`
	CallerRemaining   *float64 `json:"callerRemaining,omitempty"`
}

// LimitsConcurrency is the concurrency section of the /api/mgmt/limits response.
type LimitsConcurrency struct {
	Enabled        bool `json:"enabled"`
	Max            int  `json:"max,omitempty"`
	CallerInFlight *int `json:"callerInFlight,omitempty"`
}

// LimitsResponse is the body of GET /api/mgmt/limits.
//
// CallerIP echoes the client IP the limiter resolved for this request
// (respecting trusted_proxies), and CallerBucket says which bucket the
// caller's traffic is accounted against — "ip" or "token" — so diagnosing a
// bucketing misconfiguration is a single curl instead of a log dive.
type LimitsResponse struct {
	RateLimit    LimitsRateLimit   `json:"rateLimit"`
	Concurrency  LimitsConcurrency `json:"concurrency"`
	CallerIP     string            `json:"callerIp"`
	CallerBucket string            `json:"callerBucket"`
}

func (s *Server) getLimits(writer http.ResponseWriter, req *http.Request) error {
	cfg := s.rateLimiter.Config()
	key, kind, ip := s.rateLimiter.CallerBucket(req)
	state := s.rateLimiter.StateFor(key)

	resp := LimitsResponse{
		RateLimit:    LimitsRateLimit{Enabled: cfg.RequestsPerMinute > 0},
		Concurrency:  LimitsConcurrency{Enabled: cfg.MaxConcurrent > 0},
		CallerIP:     ip,
		CallerBucket: kind,
	}
	if resp.RateLimit.Enabled {
		resp.RateLimit.RequestsPerMinute = cfg.RequestsPerMinute
		resp.RateLimit.Burst = cfg.Burst
		remaining := state.Remaining
		resp.RateLimit.CallerRemaining = &remaining
	}
	if resp.Concurrency.Enabled {
		resp.Concurrency.Max = cfg.MaxConcurrent
		inFlight := state.InFlight
		resp.Concurrency.CallerInFlight = &inFlight
	}

	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)
	data, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = writer.Write(data)
	return err
}

func (s *Server) getVersion(writer http.ResponseWriter, _ *http.Request) error {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)

	versionInfo := version.Get()
	versionInfo.RunMode = s.config.RunMode

	data, err := json.Marshal(versionInfo)
	if err != nil {
		return err
	}

	_, err = writer.Write(data)

	return err
}

func (s *Server) serveFile(fs embed.FS, fileName string) func(writer http.ResponseWriter, _ *http.Request) error {
	return func(writer http.ResponseWriter, _ *http.Request) error {
		fileData, err := fs.ReadFile(fileName)
		if err != nil {
			http.Error(writer, "File not found", http.StatusNotFound)

			return err
		}

		writer.Header().Set("Content-Type", mime.TypeByExtension(fileName))
		writer.WriteHeader(http.StatusOK)

		if _, err := writer.Write(fileData); err != nil {
			return err
		}

		return nil
	}
}

// handlerWithDocsHost wraps the main router so a request to the docs host
// (server.docs_host, default docs.solidping.io) is redirected to the /docs path
// where the documentation site is served on every host. This lets
// docs.solidping.io/foo resolve to /docs/foo without a separate root build.
// When docs_host is empty, only the path-based /docs route is active. The host
// comparison ignores any port and is case-insensitive.
func (s *Server) handlerWithDocsHost() http.Handler {
	docsHost := strings.ToLower(strings.TrimSpace(s.config.Server.DocsHost))
	if docsHost == "" {
		return s.router
	}

	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		if docsHostMatches(req.Host, docsHost) && !strings.HasPrefix(req.URL.Path, "/docs") {
			target := "/docs" + req.URL.Path
			if req.URL.RawQuery != "" {
				target += "?" + req.URL.RawQuery
			}
			http.Redirect(writer, req, target, http.StatusFound)

			return
		}

		s.router.ServeHTTP(writer, req)
	})
}

// docsHostMatches reports whether the request Host (which may carry a port)
// equals the configured docs host, case-insensitively.
func docsHostMatches(reqHost, docsHost string) bool {
	host := reqHost
	if h, _, err := net.SplitHostPort(reqHost); err == nil {
		host = h
	}

	return strings.EqualFold(host, docsHost)
}

// serveDocsRoute is the handler for /docs and /docs/*path. It strips
// the /docs prefix and serves the matching file from the embedded docs build.
func (s *Server) serveDocsRoute(writer http.ResponseWriter, req *http.Request) error {
	s.serveDocsFile(writer, strings.TrimPrefix(req.URL.Path, "/docs"))

	return nil
}

// embedWidgetV1Path is where the status0 build emits the compiled embeddable
// status widget (see web/status0/package.json's `build:widget` script), inside
// the same dist that becomes the embedded status0res filesystem.
const embedWidgetV1Path = "status0res/embed/v1/widget.js"

// serveEmbedWidgetV1 serves the embeddable status widget (spec
// 2026-08-08-08) at /embed/v1/widget.js — the script customers paste onto
// their own sites:
//
//	<script async src="https://<host>/embed/v1/widget.js" data-page="org/slug"></script>
//
// EVERYTHING UNDER /embed/v1/ IS A FROZEN PUBLIC CONTRACT. Once a customer has
// pasted the snippet we can never take the URL away or change what the script
// does; a behavior change ships as a new /embed/v2/widget.js route alongside
// this one. The long cache lifetime (1 h, versus 60 s for the summary data the
// widget polls) is deliberate: the script is immutable within a version, only
// the data behind it moves.
func (s *Server) serveEmbedWidgetV1(writer http.ResponseWriter, req *http.Request) error {
	data, err := fs.ReadFile(s.status0FSOrDefault(), embedWidgetV1Path)
	if err != nil {
		slog.ErrorContext(req.Context(), "Embed widget asset missing", "error", err)
		http.Error(writer, "Not found", http.StatusNotFound)

		return nil
	}

	writer.Header().Set("Content-Type", contentTypeJS+"; charset=utf-8")
	writer.Header().Set("Cache-Control", "public, max-age=3600")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(data)

	return nil
}

// serveRootLLMsTxt serves the Docusaurus-generated llms.txt manifest at the
// conventional root path (GitHub issue #183), sharing the embedded-docs
// lookup, content-type, and cache-header logic used for /docs/llms.txt.
func (s *Server) serveRootLLMsTxt(writer http.ResponseWriter, _ *http.Request) error {
	s.serveDocsFile(writer, "/llms.txt")

	return nil
}

// serveRootLLMsFullTxt is the /llms-full.txt counterpart to serveRootLLMsTxt.
func (s *Server) serveRootLLMsFullTxt(writer http.ResponseWriter, _ *http.Request) error {
	s.serveDocsFile(writer, "/llms-full.txt")

	return nil
}

// serveDocsFile serves a file from the embedded Docusaurus build (docsres).
// Docusaurus is a multi-page static site (trailingSlash:false → one <page>.html
// per route), so a request path is resolved by trying, in order: the exact path
// (assets, llms.txt), <path>.html (pages and category indexes),
// <path>/index.html, then the static 404.html.
func (s *Server) serveDocsFile(writer http.ResponseWriter, urlPath string) {
	clean := strings.Trim(path.Clean("/"+urlPath), "/")

	var candidates []string
	if clean == "" {
		candidates = []string{"index.html"}
	} else {
		candidates = []string{clean, clean + ".html", path.Join(clean, "index.html")}
	}

	for _, candidate := range candidates {
		data, err := docsFiles.ReadFile(path.Join("docsres", candidate))
		if err != nil {
			continue
		}

		writeDocsFile(writer, candidate, data, http.StatusOK)

		return
	}

	if data, err := docsFiles.ReadFile(path.Join("docsres", "404.html")); err == nil {
		writeDocsFile(writer, "404.html", data, http.StatusNotFound)

		return
	}

	http.Error(writer, "Not found", http.StatusNotFound)
}

// writeDocsFile writes a docs file with a content type derived from its
// extension (falling back to content sniffing) and a cache policy that keeps
// HTML/text fresh while letting Docusaurus's content-hashed assets cache for a
// year.
func writeDocsFile(writer http.ResponseWriter, name string, data []byte, status int) {
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}

	maxAgeSeconds := 31536000 // 1 year for content-hashed assets
	if strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".txt") || strings.HasSuffix(name, ".xml") {
		maxAgeSeconds = 60
	}

	writer.Header().Set("Content-Type", contentType)
	writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAgeSeconds))
	writer.WriteHeader(status)
	_, _ = writer.Write(data)
}

// serveAppRoot determines whether to proxy to dev server or serve static files.
func (s *Server) serveAppRoot(writer http.ResponseWriter, req *http.Request) error {
	// Nothing under /api/ ever serves the SPA: an unmatched path in the API
	// namespace answers with the standard JSON error shape so non-browser
	// clients (MCP probes included) never receive text/html.
	if strings.HasPrefix(req.URL.Path, "/api/") {
		handlerBase := base.NewHandlerBase(s.config)

		return handlerBase.WriteError(writer, http.StatusNotFound, base.ErrorCodeNotFound, "API route not found")
	}

	// Redirect root to dash0 dashboard
	if req.URL.Path == "/" {
		http.Redirect(writer, req, "/dash0/", http.StatusFound)

		return nil
	}

	// Check if any redirect rule matches
	for i := range s.config.Server.Redirects {
		rule := &s.config.Server.Redirects[i]
		if strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
			return s.serveAppRedirect(writer, req, *rule, s.serveAppStatic)
		}
	}

	return s.serveAppStatic(writer, req)
}

// serveAppRedirect proxies requests to the configured dev server.
// If a fallback function is provided and the proxy fails (e.g., dev server is down),
// the fallback is used to serve from embedded static files instead of returning 502.
func (s *Server) serveAppRedirect(
	writer http.ResponseWriter,
	req *http.Request,
	rule config.RedirectRule,
	fallback func(http.ResponseWriter, *http.Request) error,
) error {
	// Build the new path by replacing the matched prefix with the target path
	newPath := rule.TargetPath + strings.TrimPrefix(req.URL.Path, rule.PathPrefix)

	slog.DebugContext(req.Context(), "Proxying request",
		"originalPath", req.URL.Path,
		"targetHost", rule.TargetHost,
		"newPath", newPath,
	)

	//nolint:exhaustruct // Only Scheme and Host are needed for reverse proxy
	targetURL := &url.URL{
		Scheme: schemeHTTP,
		Host:   rule.TargetHost,
	}

	//nolint:exhaustruct // Only Rewrite and ModifyResponse are needed
	proxy := &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			r.SetURL(targetURL)
			r.Out.URL.Path = newPath
			r.Out.URL.RawPath = newPath
		},
		ModifyResponse: func(resp *http.Response) error {
			resp.Header.Set("X-Proxied-By", "solidping-dev")
			return nil
		},
	}

	// When the dev server is unreachable, fall back to embedded static files
	if fallback != nil {
		proxy.ErrorHandler = func(writer http.ResponseWriter, errReq *http.Request, err error) {
			ctx := errReq.Context()
			slog.WarnContext(ctx, "Dev server proxy failed, falling back to embedded files",
				"error", err,
				"targetHost", rule.TargetHost,
				"path", req.URL.Path,
			)

			if fbErr := fallback(writer, req); fbErr != nil {
				slog.ErrorContext(ctx, "Fallback static serving failed", "error", fbErr)
				http.Error(writer, "Internal server error", http.StatusInternalServerError)
			}
		}
	}

	proxy.ServeHTTP(writer, req)

	return nil
}

// serveAppStatic serves static files from the embedded filesystem.
func (s *Server) serveAppStatic(writer http.ResponseWriter, req *http.Request) error {
	filePath := path.Join("res", req.URL.Path)

	slog.InfoContext(req.Context(), "Serving static file", "path", filePath)

	maxAgeSeconds := 31536000 // 1 year for assets

	// Try to read the file from the embedded filesystem
	data, err := resFiles.ReadFile(filePath)
	if err != nil {
		// If file not found, serve index.html (SPA routing)
		maxAgeSeconds = 60 // Shorter cache for index.html
		filePath = path.Join("res", "index.html")

		data, err = resFiles.ReadFile(filePath)
		if err != nil {
			slog.ErrorContext(req.Context(), "Error reading file", "error", err)
			http.Error(writer, "File not found", http.StatusNotFound)

			return nil
		}
	}

	// Determine content type based on file extension
	contentType := http.DetectContentType(data)

	switch {
	case strings.HasSuffix(filePath, ".css"):
		contentType = contentTypeCSS
	case strings.HasSuffix(filePath, ".js"):
		contentType = contentTypeJS
	case strings.HasSuffix(filePath, ".svg"):
		contentType = contentTypeSVG
	case strings.HasSuffix(filePath, ".html"):
		contentType = contentTypeHTML
	}

	writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAgeSeconds))
	writer.Header().Set("Content-Type", contentType)

	if _, err := writer.Write(data); err != nil {
		return err
	}

	return nil
}

// serveDash0Root serves the dash0 status dashboard.
func (s *Server) serveDash0Root(writer http.ResponseWriter, req *http.Request) error {
	// A bookmarked /dash0/orgs/<old slug>/... URL still works after a rename.
	if s.redirectRenamedOrgSPA(writer, req) {
		return nil
	}

	// Check if any redirect rule matches for development proxying
	for i := range s.config.Server.Redirects {
		rule := &s.config.Server.Redirects[i]
		if strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
			return s.serveAppRedirect(writer, req, *rule, s.serveDash0Static)
		}
	}

	// Serve from embedded dash0 files
	return s.serveDash0Static(writer, req)
}

// serveDash0Static serves static files from the embedded dash0res filesystem.
func (s *Server) serveDash0Static(writer http.ResponseWriter, req *http.Request) error {
	// Strip /dash0 prefix and build file path
	reqPath := strings.TrimPrefix(req.URL.Path, "/dash0")
	if reqPath == "" {
		reqPath = "/"
	}

	filePath := path.Join("dash0res", reqPath)

	slog.InfoContext(req.Context(), "Serving dash0 static file", "path", filePath)

	maxAgeSeconds := 31536000 // 1 year for assets

	// Try to read the file from the embedded filesystem
	data, err := dash0Files.ReadFile(filePath)
	if err != nil {
		// If file not found, serve index.html (SPA routing)
		maxAgeSeconds = 60 // Shorter cache for index.html
		filePath = path.Join("dash0res", "index.html")

		data, err = dash0Files.ReadFile(filePath)
		if err != nil {
			slog.ErrorContext(req.Context(), "Error reading dash0 file", "error", err)
			http.Error(writer, "File not found", http.StatusNotFound)

			return nil
		}
	}

	// Determine content type based on file extension
	contentType := http.DetectContentType(data)

	switch {
	case strings.HasSuffix(filePath, ".css"):
		contentType = contentTypeCSS
	case strings.HasSuffix(filePath, ".js"):
		contentType = contentTypeJS
	case strings.HasSuffix(filePath, ".svg"):
		contentType = contentTypeSVG
	case strings.HasSuffix(filePath, ".html"):
		contentType = contentTypeHTML
	case strings.HasSuffix(filePath, ".png"):
		contentType = contentTypePNG
	case strings.HasSuffix(filePath, ".ico"):
		contentType = contentTypeICO
	}

	writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAgeSeconds))
	writer.Header().Set("Content-Type", contentType)

	if _, err := writer.Write(data); err != nil {
		return err
	}

	return nil
}

// serveStatus0Root serves the status0 public status page app.
func (s *Server) serveStatus0Root(writer http.ResponseWriter, req *http.Request) error {
	// A pasted /status0/<old slug>/<page> link still works after a rename.
	if s.redirectRenamedOrgSPA(writer, req) {
		return nil
	}

	for i := range s.config.Server.Redirects {
		rule := &s.config.Server.Redirects[i]
		if strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
			return s.serveAppRedirect(writer, req, *rule, s.serveStatus0Static)
		}
	}

	return s.serveStatus0Static(writer, req)
}

// status0FSOrDefault returns the status0 filesystem, falling back to the real
// embedded status0Files when s.status0FS is unset (the normal, non-test case).
func (s *Server) status0FSOrDefault() fs.FS {
	if s.status0FS != nil {
		return s.status0FS
	}

	return status0Files
}

// serveStatus0Static serves static files from the embedded status0res filesystem.
func (s *Server) serveStatus0Static(writer http.ResponseWriter, req *http.Request) error {
	reqPath := strings.TrimPrefix(req.URL.Path, "/status0")
	if reqPath == "" {
		reqPath = "/"
	}

	filePath := path.Join("status0res", reqPath)

	maxAgeSeconds := 31536000 // 1 year for assets
	servingIndexFallback := false

	data, err := fs.ReadFile(s.status0FSOrDefault(), filePath)
	if err != nil {
		maxAgeSeconds = 60
		servingIndexFallback = true
		filePath = path.Join("status0res", "index.html")

		data, err = fs.ReadFile(s.status0FSOrDefault(), filePath)
		if err != nil {
			slog.ErrorContext(req.Context(), "Error reading status0 file", "error", err)
			http.Error(writer, "File not found", http.StatusNotFound)

			return nil
		}
	}

	// For a status-page path served via the SPA index.html fallback, inject
	// per-page Open Graph / Twitter Card metadata so shared links get a rich
	// preview. Non-status-page paths and missing/disabled/private pages keep
	// the generic head (no page-existence leak).
	if servingIndexFallback {
		if meta, ok := s.status0MetaForPath(req, reqPath); ok {
			data = []byte(injectStatus0Meta(string(data), &meta))
		}
	}

	contentType := http.DetectContentType(data)

	switch {
	case strings.HasSuffix(filePath, ".css"):
		contentType = contentTypeCSS
	case strings.HasSuffix(filePath, ".js"):
		contentType = contentTypeJS
	case strings.HasSuffix(filePath, ".svg"):
		contentType = contentTypeSVG
	case strings.HasSuffix(filePath, ".html"):
		contentType = contentTypeHTML
	case strings.HasSuffix(filePath, ".png"):
		contentType = contentTypePNG
	case strings.HasSuffix(filePath, ".ico"):
		contentType = contentTypeICO
	}

	writer.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", maxAgeSeconds))
	writer.Header().Set("Content-Type", contentType)

	if _, err := writer.Write(data); err != nil {
		return err
	}

	return nil
}

// runStartupJob runs the startup job synchronously to ensure critical resources
// (like the default organization) exist before workers start.
func (s *Server) runStartupJob(ctx context.Context) error {
	jobDef := &jobtypes.StartupJobDefinition{}

	jobRun, err := jobDef.CreateJobRun(json.RawMessage("{}"))
	if err != nil {
		return fmt.Errorf("failed to create startup job run: %w", err)
	}

	jctx := &jobdef.JobContext{
		DB:        s.dbService.DB(),
		DBService: s.dbService,
		Services:  s.services,
		AppConfig: s.config,
		Logger:    slog.Default().With("job", "startup"),
	}

	if err := jobRun.Run(ctx, jctx); err != nil {
		return fmt.Errorf("startup job failed: %w", err)
	}

	slog.InfoContext(ctx, "Startup job completed successfully")

	return nil
}

// serveHTTP runs the plain HTTP listener (and, when acme.enabled, the in-server
// TLS listeners) until ctx is canceled or the listener fails, then drains them.
// It is the body of Start's API-role branch, extracted so Start stays readable.
func (s *Server) serveHTTP(ctx context.Context) error {
	slog.InfoContext(ctx, "Starting HTTP server", "listen", s.config.Server.Listen)

	const readHeaderTimeout = 10 * time.Second

	topHandler := s.handlerWithCustomDomains(s.handlerWithDocsHost())

	srv := &http.Server{
		Addr:              s.config.Server.Listen,
		Handler:           topHandler,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	// In-server TLS (opt-in): two extra listeners feeding the SAME handler
	// chain, so custom-host routing applies unchanged over HTTPS.
	if err := s.startTLSEdge(ctx, topHandler); err != nil {
		return err
	}

	// Start HTTP server in a goroutine
	serverErr := make(chan error, 1)

	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.ErrorContext(ctx, "HTTP server error", "error", err)
			serverErr <- err
		}
	}()

	// Wait for shutdown signal or server error
	select {
	case <-ctx.Done():
		// Graceful shutdown initiated
		slog.InfoContext(ctx, "Shutting down server", "timeout", s.config.Server.ShutdownTimeout)
	case err := <-serverErr:
		// Server failed to start or encountered an error
		return err
	}

	// Close the realtime hub first: it terminates every held-open realtime
	// WebSocket connection so srv.Shutdown (which waits for active
	// connections) can drain instead of hanging until the shutdown timeout.
	if s.realtimeHub != nil {
		s.realtimeHub.Close()
	}

	// Shutdown HTTP server first to stop accepting new requests.
	// Using a fresh context for the shutdown timeout after main ctx is canceled.
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
	defer shutdownCancel()

	//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
	if err := srv.Shutdown(shutdownCtx); err != nil {
		//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
		slog.ErrorContext(shutdownCtx, "HTTP server shutdown error", "error", err)
	}

	if s.tlsEdge != nil {
		//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
		if err := s.tlsEdge.Shutdown(shutdownCtx); err != nil {
			//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
			slog.ErrorContext(shutdownCtx, "TLS edge shutdown error", "error", err)
		}
	}

	return nil
}

// Start starts the HTTP server and blocks until shutdown.
func (s *Server) Start(ctx context.Context) error {
	// A structural database fault is terminal: arm the latch so the first one
	// any runner sees unwinds this exact context, taking the whole process
	// through the ordinary graceful-shutdown path (HTTP drained, runners given
	// their timeout) rather than an os.Exit from a random goroutine. Armed
	// before any runner starts; Arm also fires retroactively if a fault somehow
	// beat us here.
	ctx, faultShutdown := s.armDatabaseFaultShutdown(ctx)
	defer faultShutdown()

	// Start profiler server (no-op if disabled)
	if err := s.profilerSrv.Start(ctx); err != nil {
		return fmt.Errorf("failed to start profiler server: %w", err)
	}

	// Log node configuration
	if s.config.Node.Region != "" {
		slog.InfoContext(ctx, "Starting SolidPing node", "role", s.config.Node.Role, "region", s.config.Node.Region)
	} else {
		slog.InfoContext(ctx, "Starting SolidPing node", "role", s.config.Node.Role)
	}

	// Create independent cancellable context for job runners
	// This is NOT derived from ctx so that database operations can complete
	// during shutdown even after the shutdown signal is received
	runnerCtx, cancel := context.WithCancel(context.Background())
	s.cancelCtx = cancel

	// Start MCP session cleanup
	s.mcpHandler.Start(runnerCtx) //nolint:contextcheck // runnerCtx is intentionally separate from request context

	// Start JMAP inbox supervisor (idle when email_inbox not configured).
	// runnerCtx is intentionally separate from request context.
	if s.jmapManager != nil {
		s.workersWg.Add(1)

		//nolint:contextcheck // runnerCtx is intentionally separate from request context
		go s.runJMAPManager(runnerCtx)
	}

	// Start Slack Socket Mode supervisor when configured. The supervisor
	// dials Slack's outgoing WebSocket and dispatches events through the
	// shared Dispatch* functions; the HTTPS webhook handlers stay registered
	// but receive no traffic while Socket Mode is active (Slack picks one
	// transport per app at configuration time).
	if s.slackSocketSupervisor != nil {
		s.workersWg.Add(1)

		//nolint:contextcheck // runnerCtx is intentionally separate from request context
		go s.runSlackSocketSupervisor(runnerCtx)
	}

	// Telegram boot-time sanity check + webhook self-heal (no-op unless this
	// node serves the API and Telegram is configured). Best-effort and off the
	// startup path: a Telegram outage must never stop the server from booting.
	//nolint:contextcheck // runnerCtx is intentionally separate from request context
	go bootstrapTelegram(runnerCtx, s.dbService, s.config)
	// Run startup job synchronously to ensure default org exists before workers start
	if s.config.ShouldRunJobs() {
		if err := s.runStartupJob(ctx); err != nil {
			slog.ErrorContext(ctx, "Failed to run startup job", "error", err)
		}
	}

	// Start job worker (only if role allows)
	if s.config.ShouldRunJobs() {
		s.startJobWorker(runnerCtx) //nolint:contextcheck // runnerCtx is intentionally separate from request context
	} else {
		slog.InfoContext(ctx, "Skipping job worker", "role", s.config.Node.Role)
	}

	// Start check worker (only if role allows)
	if s.config.ShouldRunChecks() {
		// Validate worker region against defined regions
		regionSvc := regions.NewService(s.dbService)
		workerRegion := s.config.Server.CheckWorker.Region
		if err := regionSvc.ValidateWorkerRegion(ctx, workerRegion); err != nil {
			return fmt.Errorf("region validation failed: %w", err)
		}
		slog.InfoContext(ctx, "Worker region validated", "region", workerRegion)

		s.startCheckWorker(runnerCtx) //nolint:contextcheck // runnerCtx is intentionally separate from request context
	} else {
		slog.InfoContext(ctx, "Skipping check worker", "role", s.config.Node.Role)
	}

	// Start HTTP server only if role allows
	if s.config.ShouldRunAPI() {
		if err := s.serveHTTP(ctx); err != nil {
			return err
		}
	} else {
		slog.InfoContext(ctx, "Skipping HTTP server", "role", s.config.Node.Role)

		// Wait for shutdown signal when not running HTTP server
		<-ctx.Done()
		slog.InfoContext(ctx, "Shutting down node", "timeout", s.config.Server.ShutdownTimeout)
	}

	// Signal runners to stop accepting new work AFTER HTTP server is shut down
	// This allows in-flight database operations to complete
	cancel()

	// Wait for all workers to complete their current work
	s.waitForRunners(ctx)

	// A fault-driven stop is a failure, not a clean shutdown: return the fault
	// so the process exits non-zero and the supervisor restarts it (which
	// re-runs migrations). A signal-driven stop still returns context.Canceled.
	if err := s.dbFault.Err(); err != nil {
		return err
	}

	return ctx.Err()
}

// armDatabaseFaultShutdown returns a derived context that the first structural
// database fault cancels, taking the process through the ordinary
// graceful-shutdown path.
func (s *Server) armDatabaseFaultShutdown(ctx context.Context) (context.Context, context.CancelFunc) {
	faultCtx, shutdown := context.WithCancel(ctx)

	s.dbFault.Arm(func() {
		slog.ErrorContext(faultCtx, "Shutting down: the database fault above cannot be recovered by retrying")
		shutdown()
	})

	return faultCtx, shutdown
}

// waitForRunners gives the runners their shutdown budget to finish in-flight
// work.
func (s *Server) waitForRunners(ctx context.Context) {
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
	defer shutdownCancel()

	done := make(chan struct{})

	go func() {
		s.workersWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		slog.InfoContext(ctx, "All runners stopped")
	case <-shutdownCtx.Done():
		slog.WarnContext(ctx, "Timeout waiting for runners, forcing shutdown")
	}
}

// startJobWorker starts the job worker with internal runner goroutines.
// runJMAPManager wraps jmap.Manager.Run for the goroutine launched in Start.
func (s *Server) runJMAPManager(ctx context.Context) {
	defer s.workersWg.Done()

	if err := s.jmapManager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(ctx, "JMAP inbox manager exited", "error", err)
	}
}

// runSlackSocketSupervisor wraps SlackSocketSupervisor.Run for the goroutine
// launched in Start.
func (s *Server) runSlackSocketSupervisor(ctx context.Context) {
	defer s.workersWg.Done()

	if err := s.slackSocketSupervisor.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		slog.WarnContext(ctx, "Slack Socket Mode supervisor exited", "error", err)
	}
}

func (s *Server) startJobWorker(ctx context.Context) {
	nbRunners := s.config.Server.JobWorker.Nb
	if nbRunners <= 0 {
		nbRunners = 2
	}

	slog.InfoContext(ctx, "Starting job worker", "nbRunners", nbRunners)

	worker := jobworker.NewJobWorker(
		s.dbService.DB(),
		s.dbService,
		s.config,
		s.services,
		s.jobSvc,
	)
	worker.SetFaultLatch(s.dbFault)

	s.workersWg.Add(1)
	go func() {
		defer s.workersWg.Done()
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "Job worker error", "error", err)
		}
	}()

	// Start the queue-depth sampler that publishes solidping_jobs_queue_depth.
	sampler := jobworker.NewQueueDepthSampler(s.jobSvc)

	s.workersWg.Add(1)
	go func() {
		defer s.workersWg.Done()
		if err := sampler.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "Job queue-depth sampler error", "error", err)
		}
	}()
}

// startCheckWorker starts the configured number of check runner goroutines.
func (s *Server) startCheckWorker(ctx context.Context) {
	nbRunners := s.config.Server.CheckWorker.Nb
	if nbRunners <= 0 {
		slog.InfoContext(ctx, "Check runners disabled (count = 0)")
		return
	}

	slog.InfoContext(ctx, "Starting check worker", "nbRunners", nbRunners)

	worker := checkworker.NewCheckWorker(
		s.dbService,
		s.config,
		s.services,
		s.services.CheckJobs,
	)
	worker.SetFaultLatch(s.dbFault)

	s.workersWg.Add(1)
	go func() {
		defer s.workersWg.Done()
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "Check worker error", "error", err)
		}
	}()
}

// Close closes the server and its database connection.
func (s *Server) Close(ctx context.Context) error {
	var closeErr error

	// Flush pending Sentry events
	const sentryFlushTimeout = 2 * time.Second
	if !sentry.Flush(sentryFlushTimeout) {
		slog.WarnContext(ctx, "Sentry flush timed out, some events may be lost")
	}

	// Flush buffered product-analytics events. No-op (and instant) when
	// analytics is not configured.
	if err := analytics.Close(ctx); err != nil {
		slog.WarnContext(ctx, "Analytics flush failed, some events may be lost", "error", err)
	}

	// Shutdown profiler
	if s.profilerSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := s.profilerSrv.Shutdown(shutdownCtx); err != nil {
			slog.ErrorContext(ctx, "Error shutting down profiler", "error", err)
			closeErr = err
		}
	}

	// Stop the realtime fan-out before the notifier it rides on: the hub
	// releases any remaining WebSocket subscribers and the publisher flushes
	// its pending hints while the bus is still up.
	if s.realtimeHub != nil {
		s.realtimeHub.Close()
	}
	if s.services != nil {
		// nil-safe; the shutdown flush deliberately runs on a background
		// context (the caller's ctx is already canceled at this point).
		s.services.Realtime.Close() //nolint:contextcheck // background flush by design
	}

	// Close notifier first (stops listening for notifications)
	if s.services != nil && s.services.EventNotifier != nil {
		if err := s.services.EventNotifier.Close(); err != nil {
			slog.ErrorContext(ctx, "Error closing event notifier", "error", err)
			closeErr = err
		}
	}

	// Close database service
	if s.dbService != nil {
		if err := s.dbService.Close(); err != nil {
			if closeErr == nil {
				closeErr = err
			}
		}
	}

	return closeErr
}

// Handler returns the HTTP handler for the server.
func (s *Server) Handler() http.Handler {
	return s.router
}

// Initialize initializes the database (runs migrations).
func (s *Server) Initialize(ctx context.Context) error {
	return s.dbService.Initialize(ctx)
}

// InitializeSystemConfig loads system configuration from the database.
// This should be called after Initialize and before Start.
// It applies system parameters from the database to the config and
// auto-generates the JWT secret if not already set.
func (s *Server) InitializeSystemConfig(ctx context.Context, cfg *config.Config) error {
	sysConfigSvc := systemconfig.NewService(s.dbService, cfg)

	if err := sysConfigSvc.Initialize(ctx); err != nil {
		return fmt.Errorf("failed to initialize system config: %w", err)
	}

	// Re-resolve and re-install the password-hashing policy now that the
	// system-parameter overlay has mutated cfg.Auth.Password.* (see spec Q2).
	reResolvePasswordPolicy(ctx, cfg)

	// Update the server's auth config if JWT secret changed
	if cfg.Auth.JWTSecret != s.config.Auth.JWTSecret {
		// The auth service was already created with the old secret.
		// A restart is required for the new secret to take effect.
		slog.InfoContext(ctx, "JWT secret updated from system config, auth service will use new secret on restart")
	}

	return nil
}

// reResolvePasswordPolicy re-resolves the process-wide password-hashing policy
// from cfg.Auth.Password after the system-parameter overlay has mutated it. The
// policy installed in NewServer (server.go:165) only reflected YAML/env, so
// without this any auth.password.* values stored via the Server Settings UI
// would be ignored even across a restart (see spec Q2).
//
// It is deliberately best-effort and NON-fatal: a malformed stored value (e.g.
// set via the raw API, bypassing write validation) must never brick startup, so
// on error we warn and keep the prior policy. The early NewServer install
// remains the default for any hashing before this point. The overlaid block is
// validated first (same bounds as config load) so a sub-floor value that slipped
// past the write handler can't install a degraded policy either.
func reResolvePasswordPolicy(ctx context.Context, cfg *config.Config) {
	if err := config.ValidatePasswordConfigBlock(&cfg.Auth.Password); err != nil {
		slog.WarnContext(ctx, "password policy from system params invalid; keeping prior policy", "error", err)

		return
	}

	pol, err := passwords.PolicyFromConfig(&cfg.Auth)
	if err != nil {
		slog.WarnContext(ctx, "password policy from system params invalid; keeping prior policy", "error", err)

		return
	}

	passwords.SetDefaultPolicy(pol)
}

// MaybeAutoMigrateEncryption sweeps existing plaintext secrets into the
// encrypted columns when a master key is configured and AutoMigrate is on
// (default). Idempotent — only rows still in plaintext are touched. Logs
// a summary; errors propagate so the operator notices at startup.
//
// When credentials are *disabled* and the DB already holds encrypted rows
// (operator removed the master key), we log a loud WARN — those rows
// can't be decrypted at run time and workers will skip them.
func (s *Server) MaybeAutoMigrateEncryption(ctx context.Context) error {
	if s.services == nil || s.services.Credentials == nil || !s.services.Credentials.Enabled() {
		s.warnIfEncryptedRowsExist(ctx)

		return s.maybeSplitPlaintextSecrets(ctx)
	}

	if !s.config.Encryption.AutoMigrate {
		slog.InfoContext(ctx, "encrypt-credentials auto-migrate disabled by config")

		return nil
	}

	stats, err := credmigrate.Run(ctx, s.dbService, s.services.Credentials, credmigrate.Options{
		Logger: slog.Default(),
	})
	if err != nil {
		return fmt.Errorf("auto-migrate credentials: %w", err)
	}

	if stats.ChecksMigrated > 0 || stats.ConnectionsMigrated > 0 {
		slog.InfoContext(ctx, "encrypted plaintext credentials at startup",
			"checksMigrated", stats.ChecksMigrated,
			"connectionsMigrated", stats.ConnectionsMigrated)
	}

	// One-shot URL backfill: demote webhook url / *_webhook_url from the
	// encrypted blob to public settings so the edit form renders them.
	// Idempotent — skips rows already reconciled.
	recStats, recErr := credmigrate.ReconcileConnectionRegistry(
		ctx, s.dbService, s.services.Credentials, credmigrate.Options{Logger: slog.Default()},
	)
	if recErr != nil {
		return fmt.Errorf("reconcile connection url registry: %w", recErr)
	}

	if recStats.ConnectionsReconciled > 0 {
		slog.InfoContext(ctx, "reconciled connection URL fields to public settings at startup",
			"connectionsReconciled", recStats.ConnectionsReconciled)
	}

	return nil
}

// ReconcileCheckJobSchedules recomputes period, scheduled_at, and
// effective_scheduled_at for every check_job whose period no longer matches
// its check's period (spec 2026-07-20-05). One-shot at startup: multi-region
// checks written before the per-region-full-period change carry a stale
// basePeriod×region_count job period and would keep it until the check is next
// edited. Idempotent — a database whose jobs already match reconciles nothing.
func (s *Server) ReconcileCheckJobSchedules(ctx context.Context) error {
	if s.dbService == nil || s.services == nil {
		return nil
	}

	checksService := checks.NewService(
		s.dbService, s.services.EventNotifier, s.services.Credentials, s.services.Entitlements)

	reconciled, err := checksService.ReconcileStaleJobSchedules(ctx)
	if err != nil {
		return fmt.Errorf("reconcile stale check job schedules: %w", err)
	}

	if reconciled > 0 {
		slog.InfoContext(ctx, "reconciled stale multi-region check job schedules at startup",
			"checksReconciled", reconciled)
	}

	return nil
}

// maybeSplitPlaintextSecrets is the no-master-key counterpart to the encryption
// auto-migrate: with encryption disabled, it moves any secret still sitting in a
// PUBLIC column (checks.config, check_jobs.config, connection settings) into a
// plaintext envelope in the matching private column, closing the API leak for
// databases written before that fix. Read-time redaction already protects the
// API surface; this cleans the storage so the leak has no source at all.
//
// Gated on the same AutoMigrate flag as the encrypted sweep so an operator who
// opts out of automatic startup row rewrites gets none. Idempotent.
func (s *Server) maybeSplitPlaintextSecrets(ctx context.Context) error {
	if s.dbService == nil {
		return nil
	}

	if !s.config.Encryption.AutoMigrate {
		return nil
	}

	stats, err := credmigrate.RunPlaintext(ctx, s.dbService, credmigrate.Options{Logger: slog.Default()})
	if err != nil {
		return fmt.Errorf("split plaintext credentials: %w", err)
	}

	if stats.Migrated() > 0 {
		slog.InfoContext(ctx, "split public-column plaintext secrets into private column at startup",
			"checksMigrated", stats.ChecksMigrated,
			"checkJobsMigrated", stats.CheckJobsMigrated,
			"connectionsMigrated", stats.ConnectionsMigrated)
	}

	return nil
}

// warnIfEncryptedRowsExist scans the secret-bearing tables for non-NULL
// private columns when encryption is disabled and emits a single WARN
// per startup. The query is a counting scan, not a row fetch — cheap
// enough to run unconditionally.
func (s *Server) warnIfEncryptedRowsExist(ctx context.Context) {
	if s.dbService == nil {
		return
	}

	bun := s.dbService.DB()

	checkCount, err := bun.NewSelect().Model((*models.Check)(nil)).
		Where("config_private IS NOT NULL AND config_private != ''").Count(ctx)
	if err != nil {
		return
	}

	connCount, err := bun.NewSelect().Model((*models.Integration)(nil)).
		Where("settings_private IS NOT NULL AND settings_private != ''").Count(ctx)
	if err != nil {
		return
	}

	if checkCount == 0 && connCount == 0 {
		return
	}

	//nolint:sloglint // startup-only; no request context
	slog.Warn("encrypted rows present but credentials encryption is disabled — these rows are unreadable",
		"checks", checkCount, "connections", connCount,
		"how_to_fix", "set SP_ENCRYPTION_MASTER_KEY (or SP_ENCRYPTION_MASTER_KEY_FILE) to the original key")
}

// InitializeTestData creates test data for test mode.
// This should be called after Initialize and before Start.
func (s *Server) InitializeTestData(ctx context.Context) error {
	if s.config.RunMode != "test" {
		return nil
	}

	slog.InfoContext(ctx, "Test mode detected, creating test data")

	return testdata.CreateTestData(ctx, s.dbService)
}

//nolint:ireturn // Returning interface is intentional for testing

// DBService returns the database service instance (used for testing).
func (s *Server) DBService() db.Service {
	return s.dbService
}

// Services returns the services registry (used for testing).
func (s *Server) Services() *services.Registry {
	return s.services
}

// JobSvc returns the job service (used for testing).
func (s *Server) JobSvc() jobsvc.Service {
	return s.jobSvc
}

// Sentinel errors surfaced by newFreeboxConnectionResolver. Defining
// them statically lets callers branch on them and keeps err113 happy
// without spelling out every formatted variant.
var (
	errFreeboxWrongType          = errors.New("connection is not a freebox connection")
	errFreeboxEncryptionDisabled = errors.New(
		"connection has encrypted settings but credentials service is disabled",
	)
	errFreeboxNoAppToken = errors.New("connection has no app_token")
)

// newFreeboxConnectionResolver returns a ConnectionResolver closure that
// looks up an IntegrationConnection by UID, asserts the type is
// `freebox`, and merges the decrypted app_token from settings_private
// with the public base URL / app_id fields. Returns an error when the
// connection is missing, of the wrong type, or has no app_token (a
// connection still in the pairing state).
func newFreeboxConnectionResolver(
	dbService db.Service, credSvc credentials.Service,
) checkfreeboxline.ConnectionResolver {
	return func(ctx context.Context, connectionUID string) (*checkfreeboxline.ResolvedConnection, error) {
		conn, err := dbService.GetChannel(ctx, connectionUID)
		if err != nil {
			return nil, fmt.Errorf("get channel %s: %w", connectionUID, err)
		}

		if conn.Type != models.ConnectionTypeFreebox {
			return nil, fmt.Errorf(
				"%w: connection %s is %q, expected %q",
				errFreeboxWrongType, connectionUID, conn.Type, models.ConnectionTypeFreebox,
			)
		}

		settings, err := models.FreeboxSettingsFromJSONMap(conn.Settings)
		if err != nil {
			return nil, fmt.Errorf("parse freebox settings: %w", err)
		}

		// The app_token lives in the encrypted private side. With no
		// encryption configured the channel handler stores plaintext in
		// the public settings under the same key — we honor both paths.
		appToken := ""

		if conn.SettingsPrivate != nil && *conn.SettingsPrivate != "" {
			if !credSvc.Enabled() {
				return nil, fmt.Errorf("%w: %s", errFreeboxEncryptionDisabled, connectionUID)
			}

			privMap, decErr := credSvc.DecryptForOrg(ctx, conn.OrganizationUID, *conn.SettingsPrivate)
			if decErr != nil {
				return nil, fmt.Errorf("decrypt freebox app_token: %w", decErr)
			}

			if v, ok := privMap["appToken"].(string); ok {
				appToken = v
			}
		}

		if appToken == "" {
			// Plaintext-fallback path: pre-encryption installs and tests
			// with credentials disabled keep the app_token under the same
			// key in the public Settings map.
			if v, ok := conn.Settings["appToken"].(string); ok {
				appToken = v
			}
		}

		if appToken == "" {
			return nil, fmt.Errorf(
				"%w: connection %s (pairing status: %s)",
				errFreeboxNoAppToken, connectionUID, settings.Status,
			)
		}

		return &checkfreeboxline.ResolvedConnection{
			BaseURL:  settings.BaseURL,
			AppID:    settings.AppID,
			AppToken: appToken,
		}, nil
	}
}

// BuildCredentialsService loads the KEK (env or file), constructs the
// credentials service, and wires the per-org DEK store against the
// existing parameters table. Returns a disabled service (no error) when
// no master key is configured — that is the documented fallback.
//
// Exported so the encrypt-credentials CLI command can build the same
// service the server uses, without duplicating the wiring.
func BuildCredentialsService(cfg *config.Config, dbService db.Service) (credentials.Service, error) {
	kek, err := loadEncryptionMasterKey(cfg)
	if err != nil {
		return nil, err
	}

	store := credentials.ParamStore{
		Load: func(ctx context.Context, orgUID, key string) (json.RawMessage, bool, error) {
			param, getErr := dbService.GetOrgParameter(ctx, orgUID, key)
			if getErr != nil {
				return nil, false, getErr
			}
			if param == nil {
				return nil, false, nil
			}
			raw, mErr := json.Marshal(param.Value)
			if mErr != nil {
				return nil, false, mErr
			}
			return raw, true, nil
		},
		Save: func(ctx context.Context, orgUID, key string, value any, secret bool) error {
			return dbService.SetOrgParameter(ctx, orgUID, key, value, secret)
		},
	}

	return credentials.NewService(kek, store)
}

// loadEncryptionMasterKey returns the raw KEK bytes from the env-derived
// config. Returns nil (with no error) when no key is configured — that
// disables encryption. Both base64 and base64-without-padding are
// accepted, matching what kubectl create secret typically emits.
func loadEncryptionMasterKey(cfg *config.Config) ([]byte, error) {
	source := cfg.Encryption.MasterKey
	if cfg.Encryption.MasterKeyFile != "" {
		// File path wins when both are set — matches the spec contract.
		// Read lazily here so a missing file fails loudly at startup
		// rather than silently disabling encryption.
		bytes, err := readMasterKeyFile(cfg.Encryption.MasterKeyFile)
		if err != nil {
			return nil, fmt.Errorf("read master key file: %w", err)
		}
		source = bytes
	}

	if source == "" {
		return nil, nil
	}

	return credentials.DecodeMasterKey(source)
}

// readMasterKeyFile slurps the file at path and returns its trimmed
// contents. Trim is important — env-mounted secrets often have a trailing
// newline. The contents are still treated as a base64 string by the
// caller, so any other whitespace would break decoding anyway.
func readMasterKeyFile(path string) (string, error) {
	bytes, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(bytes)), nil
}
