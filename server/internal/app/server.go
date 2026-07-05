// Package app provides the HTTP server and application setup.
package app

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/uptrace/bunrouter"
	k8sclient "k8s.io/client-go/kubernetes"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/checkfreeboxline"
	"github.com/fclairamb/solidping/server/internal/checkers/checkkubernetes"
	"github.com/fclairamb/solidping/server/internal/checkworker"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/credmigrate"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	entitlementsapi "github.com/fclairamb/solidping/server/internal/entitlements"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/availability"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
	"github.com/fclairamb/solidping/server/internal/handlers/base"
	"github.com/fclairamb/solidping/server/internal/handlers/checkchannels"
	"github.com/fclairamb/solidping/server/internal/handlers/checkdependencies"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/checkjobs"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checktypes"
	"github.com/fclairamb/solidping/server/internal/handlers/discovery"
	"github.com/fclairamb/solidping/server/internal/handlers/emailcheck"
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
	"github.com/fclairamb/solidping/server/internal/handlers/realtimews"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/handlers/severities"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/handlers/statussubscribers"
	"github.com/fclairamb/solidping/server/internal/handlers/statusupdates"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/handlers/testapi"
	"github.com/fclairamb/solidping/server/internal/handlers/usernotifications"
	webpushhandler "github.com/fclairamb/solidping/server/internal/handlers/webpush"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	integrationk8s "github.com/fclairamb/solidping/server/internal/integrations/kubernetes"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
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
	"github.com/fclairamb/solidping/server/internal/utils/clock"
	"github.com/fclairamb/solidping/server/internal/utils/passwords"
	"github.com/fclairamb/solidping/server/internal/version"
	webpushpkg "github.com/fclairamb/solidping/server/internal/webpush"
	"github.com/fclairamb/solidping/server/test/testdata"
)

const (
	// embeddedPostgresPort is the default port for embedded PostgreSQL.
	embeddedPostgresPort = 5434

	// Content type constants for static file serving.
	contentTypeCSS  = "text/css"
	contentTypeJS   = "application/javascript"
	contentTypeSVG  = "image/svg+xml"
	contentTypeHTML = "text/html"
	contentTypePNG  = "image/png"
	contentTypeICO  = "image/x-icon"
)

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
	router                *bunrouter.Router
	config                *config.Config
	authService           *auth.Service
	mcpHandler            *mcp.Handler
	profilerSrv           *profiler.Server
	jmapManager           *jmap.Manager
	slackSocketSupervisor *slack.SlackSocketSupervisor
	rateLimiter           *middleware.RateLimiter // For the /api/mgmt/limits introspection handler
	realtimeHub           *realtime.Hub           // Live hint stream fan-out (nil when realtime disabled)
	cancelCtx             context.CancelFunc
	workersWg             sync.WaitGroup // Tracks workers
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
			Embedded:    true,
			EmbeddedDir: "/tmp/solidping-postgres-test",
			Port:        embeddedPostgresPort,
			LogSQL:      cfg.Database.LogSQL,
			RunMode:     cfg.RunMode,
			Reset:       cfg.Database.Reset,
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
	case "sqlite-memory":
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
	// call, so no external wakeup channel is needed here.
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
	)
	svcList.Entitlements = entitlementsService

	// Create auth service. The entitlements service gates SSO membership
	// caps inside ensureMembership (every OAuth callback) and inside
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
		dbService:   dbService,
		jobSvc:      jobService,
		services:    svcList,
		config:      cfg,
		authService: authService,
		profilerSrv: profiler.New(&cfg.Profiler),
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

// SetupRoutes builds the HTTP router and registers every handler. It must
// be called after InitializeSystemConfig so handlers see the post-overlay
// config (e.g. PasskeyService deriving its RP ID from cfg.Server.BaseURL,
// which the system-parameters table can override at runtime).
//
//nolint:funlen,cyclop // Route registration function naturally grows with new routes
func (s *Server) SetupRoutes(ctx context.Context) {
	router := bunrouter.New()
	rateLimiter := middleware.NewRateLimiter(s.config.Server.RateLimiting, ctx)
	s.rateLimiter = rateLimiter
	// ctx for RequestTimeout is taken from each bunrouter.Request inside the
	// middleware closure, not threaded through here; the contextcheck linter
	// can't see that.
	timeoutMW := middleware.RequestTimeout(s.config.Server.MaxRequestDuration) //nolint:contextcheck
	mainGroup := router.Use(s.corsMiddleware).Use(middleware.SentryMiddleware()).Use(s.loggingMiddleware).
		Use(middleware.HTTPMetrics).
		Use(timeoutMW).
		Use(rateLimiter.RateLimit).
		Use(rateLimiter.ConcurrencyLimit)

	// API routes
	api := mainGroup.NewGroup("/api/v1")
	api.OPTIONS("/*path", func(_ http.ResponseWriter, req bunrouter.Request) error {
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
	rootAuthProtected.DELETE("/tokens/:tokenUid", authHandler.RevokeToken)
	rootAuthProtected.POST("/passkeys/register/begin", passkeyHandler.RegisterBegin)
	rootAuthProtected.POST("/passkeys/register/finish", passkeyHandler.RegisterFinish)
	rootAuthProtected.GET("/passkeys", passkeyHandler.List)
	rootAuthProtected.PATCH("/passkeys/:uid", passkeyHandler.Rename)
	rootAuthProtected.DELETE("/passkeys/:uid", passkeyHandler.Delete)
	rootAuthProtected.POST("/membership-requests", authHandler.CreateMembershipRequestHandler)
	rootAuthProtected.GET("/membership-requests", authHandler.ListOwnMembershipRequestsHandler)
	rootAuthProtected.DELETE("/membership-requests/:uid", authHandler.CancelMembershipRequestHandler)

	// Org creation (protected)
	orgsGroup := api.NewGroup("/orgs").Use(authMiddleware.RequireAuth)
	orgsGroup.POST("", authHandler.CreateOrg)

	// Org-scoped token management (protected)
	orgTokens := api.NewGroup("/orgs/:org/tokens").Use(authMiddleware.RequireAuth)
	orgTokens.GET("", authHandler.GetOrgTokens)
	orgTokens.POST("", authHandler.CreateToken)

	// Org invitations (protected, admin-only checked in handler)
	orgInvitations := api.NewGroup("/orgs/:org/invitations").Use(authMiddleware.RequireAuth)
	orgInvitations.GET("", authHandler.ListInvitations)
	orgInvitations.POST("", authHandler.CreateInvitation)
	orgInvitations.DELETE("/:uid", authHandler.RevokeInvitation)

	// Org settings (protected, admin-only checked in handler)
	orgSettings := api.NewGroup("/orgs/:org/settings").Use(authMiddleware.RequireAuth)
	orgSettings.GET("", authHandler.GetOrgSettings)
	orgSettings.PATCH("", authHandler.UpdateOrgSettings)

	// Org membership requests (protected, admin-only checked in handler)
	orgMembershipRequests := api.NewGroup("/orgs/:org/membership-requests").Use(authMiddleware.RequireAuth)
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

	// Auth providers endpoint (public)
	providersHandler := auth.NewProvidersHandler(s.config, passkeyService.Enabled)
	api.GET("/auth/providers", providersHandler.ListProviders)

	// Check types service (constructed early so MCP can use it too)
	activationResolver := checkerdef.NewActivationResolver(s.config.Checkers)
	checkTypesService := checktypes.NewService(activationResolver, s.config.Server.BaseURL)

	// MCP endpoint (auth via PAT token, org derived from token)
	s.mcpHandler = mcp.NewHandler(
		s.dbService, s.services.EventNotifier, s.jobSvc, checkTypesService,
		s.services.Credentials, s.services.Entitlements, s.services.Realtime,
	)
	mcpGroup := api.NewGroup("/mcp").Use(authMiddleware.RequireMCPAuth)
	mcpGroup.POST("", s.mcpHandler.Handle)

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

	// Job routes (auth required for org-scoped routes)
	jobHandler := jobs.NewHandler(s.jobSvc)
	orgJobsGroup := api.NewGroup("/orgs/:org/jobs").Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
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
		Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
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
	orgCheckTypes := api.NewGroup("/orgs/:org/check-types").Use(authMiddleware.RequireAuth)
	orgCheckTypes.GET("", checkTypesHandler.ListOrgCheckTypes)

	// Check routes (authentication required)
	checksService := checks.NewService(
		s.dbService, s.services.EventNotifier, s.services.Credentials, s.services.Entitlements)
	checksHandler := checks.NewHandler(checksService, s.config)
	orgChecks := api.NewGroup("/orgs/:org/checks").Use(authMiddleware.RequireAuth)
	orgChecks.GET("", checksHandler.ListChecks)
	orgChecks.POST("", checksHandler.CreateCheck)

	// Config-as-code surface (export/import/apply) is admin-only: import and
	// apply mutate the whole check set, and apply can delete-by-absence.
	// Export is gated alongside them (it was RequireAuth-only — a latent gap;
	// see specs/todos/2026-06-20-05-config-as-code.md). Apply is the reconcile
	// sibling of import with dry-run/prune/deletion-cap guardrails.
	orgChecksAdmin := api.NewGroup("/orgs/:org/checks").
		Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess, authMiddleware.RequireOrgAdmin)
	orgChecksAdmin.GET("/export", checksHandler.ExportChecks)
	orgChecksAdmin.POST("/import", checksHandler.ImportChecks)
	orgChecksAdmin.POST("/apply", checksHandler.ApplyChecks)

	orgChecks.POST("/validate", checksHandler.ValidateCheck)
	orgChecks.GET("/:checkUid", checksHandler.GetCheck)
	orgChecks.PUT("/:slug", checksHandler.UpsertCheck)
	orgChecks.PATCH("/:checkUid", checksHandler.UpdateCheck)
	orgChecks.DELETE("/:checkUid", checksHandler.DeleteCheck)
	orgChecks.POST("/:checkUid/clone", checksHandler.CloneCheck)

	// Network discovery routes (authentication + org access required)
	discoverySvc := discovery.NewService(
		s.dbService.DB(), s.dbService, checksService, s.jobSvc, s.services.Credentials,
	)
	discoveryHandler := discovery.NewHandler(discoverySvc, s.config)
	orgDiscovery := api.NewGroup("/orgs/:org/discovery").Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
	discoveryHandler.RegisterRoutes(orgDiscovery)

	// Label autocomplete routes
	labelsService := labels.NewService(s.dbService)
	labelsHandler := labels.NewHandler(labelsService, s.config)
	orgLabels := api.NewGroup("/orgs/:org/labels").Use(authMiddleware.RequireAuth)
	orgLabels.GET("", labelsHandler.ListLabels)

	// Region routes
	regionsService := regionshandler.NewService(s.dbService)
	regionsHandler := regionshandler.NewHandler(regionsService, s.config)
	api.GET("/regions", regionsHandler.ListGlobalRegions) // Public, no auth
	orgRegions := api.NewGroup("/orgs/:org/regions").Use(authMiddleware.RequireAuth)
	orgRegions.GET("", regionsHandler.ListOrgRegions)

	// Check group routes (authentication required)
	checkGroupsService := checkgroups.NewService(s.dbService)
	checkGroupsHandler := checkgroups.NewHandler(checkGroupsService, s.config)
	orgCheckGroups := api.NewGroup("/orgs/:org/check-groups").Use(authMiddleware.RequireAuth)
	orgCheckGroups.GET("", checkGroupsHandler.ListCheckGroups)
	orgCheckGroups.POST("", checkGroupsHandler.CreateCheckGroup)
	orgCheckGroups.GET("/:uid", checkGroupsHandler.GetCheckGroup)
	orgCheckGroups.PATCH("/:uid", checkGroupsHandler.UpdateCheckGroup)
	orgCheckGroups.DELETE("/:uid", checkGroupsHandler.DeleteCheckGroup)

	// Severity routes (authentication required). Per-org channel-set
	// primitive consumed by escalation step fan-out — spec 2026-05-08-03.
	severitiesService := severities.NewService(s.dbService)
	severitiesHandler := severities.NewHandler(severitiesService, s.config)
	orgSeverities := api.NewGroup("/orgs/:org/severities").Use(authMiddleware.RequireAuth)
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
	orgDeps := api.NewGroup("/orgs/:org/dependencies").Use(authMiddleware.RequireAuth)
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
	badgesService := badges.NewService(s.dbService)
	badgesHandler := badges.NewHandler(badgesService, s.config)
	api.GET("/orgs/:org/checks/:check/badges/:components", badgesHandler.GetBadge)

	// Heartbeat ingestion routes (public, token-based auth)
	heartbeatService := heartbeat.NewService(s.dbService, s.jobSvc, s.services.Realtime)
	heartbeatHandler := heartbeat.NewHandler(heartbeatService, s.config)
	api.POST("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)
	api.GET("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)

	// Edge worker API routes (worker token auth, no user auth)
	workersService := workers.NewService(
		s.dbService,
		s.services.CheckJobs,
		incidents.NewService(s.dbService, s.jobSvc, s.services.Clock, s.services.Realtime),
		s.services.Credentials,
	)
	workersHandler := workers.NewHandler(workersService, s.config)
	workerAPI := api.NewGroup("/workers")
	workerAPI.POST("/register", workersHandler.Register)
	workerAPI.POST("/heartbeat", workersHandler.Heartbeat)
	workerAPI.POST("/claim-jobs", workersHandler.ClaimJobs)
	workerAPI.POST("/submit-result", workersHandler.SubmitResult)

	// Results routes (authentication required)
	resultsService := results.NewService(s.dbService)
	resultsHandler := results.NewHandler(resultsService, s.config)
	orgResults := api.NewGroup("/orgs/:org/results").Use(authMiddleware.RequireAuth)
	orgResults.GET("", resultsHandler.ListResults)

	// Per-check single result fetch (with fallback to covering aggregation)
	orgChecksResults := api.NewGroup("/orgs/:org/checks/:check/results").Use(authMiddleware.RequireAuth)
	orgChecksResults.GET("/:uid", resultsHandler.GetResult)

	// Per-check availability statistics (real per-period probe-ratio + incidents)
	availabilityService := availability.NewService(s.dbService)
	availabilityHandler := availability.NewHandler(availabilityService, s.config)
	orgChecksAvail := api.NewGroup("/orgs/:org/checks/:check/availability").Use(authMiddleware.RequireAuth)
	orgChecksAvail.GET("", availabilityHandler.GetAvailability)

	// Incidents routes (authentication required)
	incidentsService := incidents.NewService(s.dbService, s.jobSvc, s.services.Clock, s.services.Realtime)
	incidentsHandler := incidents.NewHandler(incidentsService, s.config)
	orgIncidents := api.NewGroup("/orgs/:org/incidents").Use(authMiddleware.RequireAuth)
	orgIncidents.GET("", incidentsHandler.ListIncidents)
	orgIncidents.GET("/:uid", incidentsHandler.GetIncident)
	orgIncidents.POST("/:uid/ack", incidentsHandler.AcknowledgeIncident)
	orgIncidents.POST("/:uid/unack", incidentsHandler.UnacknowledgeIncident)
	orgIncidents.POST("/:uid/snooze", incidentsHandler.SnoozeIncident)
	orgIncidents.POST("/:uid/unsnooze", incidentsHandler.UnsnoozeIncident)
	orgIncidents.POST("/:uid/resolve", incidentsHandler.ResolveIncident)

	// Magic-link ack — public route (the signed token authenticates).
	// Returns text/html so it renders in a browser opened from a mail client.
	api.GET("/orgs/:org/incidents/:uid/ack", incidentsHandler.AcknowledgeIncidentByLink)

	// Incident notifications read API (authentication required)
	incidentNotifService := incidentnotifications.NewService(s.dbService)
	incidentNotifHandler := incidentnotifications.NewHandler(incidentNotifService, s.config)
	orgIncidents.GET("/:uid/notifications", incidentNotifHandler.ListForIncident)
	orgIncidents.GET("/:uid/notifications/:notifUid", incidentNotifHandler.GetForIncident)
	api.NewGroup("/orgs/:org/users").Use(authMiddleware.RequireAuth).
		GET("/:uid/notifications", incidentNotifHandler.ListForUser)
	api.NewGroup("/orgs/:org/me").Use(authMiddleware.RequireAuth).
		GET("/notifications", incidentNotifHandler.ListForMe)
	// Org-level notification endpoints (flat, no incident required)
	orgNotifs := api.NewGroup("/orgs/:org/notifications").Use(authMiddleware.RequireAuth)
	orgNotifs.GET("", incidentNotifHandler.ListByOrg)
	orgNotifs.GET("/:notifUid", incidentNotifHandler.GetByOrg)

	// On-call schedules (authentication required)
	onCallService := oncallschedules.NewService(s.dbService)
	onCallHandler := oncallschedules.NewHandler(onCallService, s.dbService, s.config)
	orgOnCall := api.NewGroup("/orgs/:org/on-call-schedules").Use(authMiddleware.RequireAuth)
	orgOnCall.GET("", onCallHandler.ListSchedules)
	orgOnCall.POST("", onCallHandler.CreateSchedule)
	orgOnCall.GET("/:slug", onCallHandler.GetSchedule)
	orgOnCall.PATCH("/:slug", onCallHandler.UpdateSchedule)
	orgOnCall.DELETE("/:slug", onCallHandler.DeleteSchedule)
	orgOnCall.GET("/:slug/preview", onCallHandler.PreviewSchedule)
	orgOnCall.GET("/:slug/overrides", onCallHandler.ListOverrides)
	orgOnCall.POST("/:slug/overrides", onCallHandler.CreateOverride)
	orgOnCall.DELETE("/:slug/overrides/:overrideUid", onCallHandler.DeleteOverride)
	orgOnCall.POST("/:slug/ical-feed/enable", onCallHandler.EnableICalFeed)
	orgOnCall.POST("/:slug/ical-feed/disable", onCallHandler.DisableICalFeed)
	orgOnCall.POST("/:slug/ical-feed/rotate", onCallHandler.RotateICalFeed)

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
	orgEscalation := api.NewGroup("/orgs/:org/escalation-policies").Use(authMiddleware.RequireAuth)
	orgEscalation.GET("", escalationHandler.ListPolicies)
	orgEscalation.POST("", escalationHandler.CreatePolicy)
	orgEscalation.GET("/:slug", escalationHandler.GetPolicy)
	orgEscalation.PATCH("/:slug", escalationHandler.UpdatePolicy)
	orgEscalation.DELETE("/:slug", escalationHandler.DeletePolicy)

	// User notification routes (authentication required)
	userNotifService := usernotifications.NewService(s.dbService)
	emailAdapter := usernotifications.NewEmailSenderAdapter(s.services.EmailSender)
	slackAdapter := usernotifications.NewSlackDMSenderAdapter()
	userNotifHandler := usernotifications.NewHandler(
		userNotifService, s.config, emailAdapter, slackAdapter, s.services.WebPushOptions,
	)
	orgUserNotif := api.NewGroup("/orgs/:org/users/me").Use(authMiddleware.RequireAuth)
	orgUserNotif.GET("/notification-routes", userNotifHandler.ListRoutes)
	orgUserNotif.POST("/notification-contacts", userNotifHandler.CreateContact)
	orgUserNotif.PATCH("/notification-routes/:routeUid", userNotifHandler.PatchRoute)
	orgUserNotif.DELETE("/notification-contacts/:contactUid", userNotifHandler.DeleteContact)
	orgUserNotif.POST("/notification-routes/:routeUid/test", userNotifHandler.TestRoute)

	// Events routes (authentication required)
	eventsService := events.NewService(s.dbService)
	eventsHandler := events.NewHandler(eventsService, s.config)
	orgEvents := api.NewGroup("/orgs/:org/events").Use(authMiddleware.RequireAuth)
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
	orgFiles := api.NewGroup("/orgs/:org/files").Use(authMiddleware.RequireAuth)
	orgFiles.GET("", filesHandler.List)
	orgFiles.GET("/:uid", filesHandler.Get)
	orgFiles.GET("/:uid/content", filesHandler.GetContent)
	orgFiles.DELETE("/:uid", filesHandler.Delete)
	pubFiles := mainGroup.NewGroup("/pub/files")
	pubFiles.GET("/:uid", filesHandler.PublicGet)

	// Bug report (public POST under /api/mgmt) and features endpoint (auth)
	feedbackService := feedback.NewService(s.dbService, filesService, s.config, nil)
	feedbackHandler := feedback.NewHandler(feedbackService, s.authService, s.config)
	featuresHandler := features.NewHandler(s.config)
	api.NewGroup("/features").Use(authMiddleware.RequireAuth).GET("", featuresHandler.GetFeatures)

	// Members routes (authentication required)
	membersService := members.NewService(s.dbService)
	membersHandler := members.NewHandler(membersService, s.config)
	orgMembers := api.NewGroup("/orgs/:org/members").Use(authMiddleware.RequireAuth)
	orgMembers.GET("", membersHandler.ListMembers)
	orgMembers.POST("", membersHandler.AddMember)
	orgMembers.GET("/:uid", membersHandler.GetMember)
	orgMembers.PATCH("/:uid", membersHandler.UpdateMember)
	orgMembers.DELETE("/:uid", membersHandler.RemoveMember)

	// System parameters routes (super admin only)
	systemService := system.NewService(s.dbService)

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

	// Org entitlements routes. The handler does its own auth gating
	// (service token preferred for SaaS billing service; admin user
	// fallback gated by entitlements.admin_writes_enabled). The billing
	// service authenticates with the entitlements.service_token shared
	// secret (not a JWT). ServiceTokenBypass marks a matching request as a
	// trusted service so the following RequireAuth + RequireOrgAccess become
	// no-ops (cross-org writes); every other caller authenticates normally.
	entitlementsHandler := entitlements.NewHandler(s.services.Entitlements, s.dbService, s.config)
	orgEntitlements := api.NewGroup("/orgs/:org/entitlements").
		//nolint:contextcheck // factory marks the request context; bunrouter threads it via req.WithContext down the chain
		Use(authMiddleware.ServiceTokenBypass(entitlements.ParamServiceToken)).
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireOrgAccess)
	orgEntitlements.GET("", entitlementsHandler.Get)
	orgEntitlements.PUT("", entitlementsHandler.Put)
	orgEntitlements.PATCH("", entitlementsHandler.Patch)
	orgEntitlements.GET("/audits", entitlementsHandler.ListAudits)

	// Web Push routes (authentication required).
	webpushHandler := webpushhandler.NewHandler(s.config)
	orgWebPush := api.NewGroup("/orgs/:org/webpush").Use(authMiddleware.RequireAuth)
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
		group := api.NewGroup(prefix).Use(authMiddleware.RequireAuth)
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

	// Freebox pairing endpoints — separate from the generic CRUD because
	// they wrap the multi-step LCD-approval handshake. POST creates the
	// integration in `pairing` status and asks the Freebox for an
	// app_token; GET polls until the user approves the prompt.
	orgFreebox := api.NewGroup("/orgs/:org/integrations/freebox").Use(authMiddleware.RequireAuth)
	orgFreebox.POST("/pair", integrationsHandler.StartFreeboxPairing)
	orgFreebox.GET("/pair/:uid/status", integrationsHandler.GetFreeboxPairingStatus)
	// LAN discovery: returns the list of hosts currently visible to the
	// Freebox so the dashboard can pre-fill ICMP checks without typing.
	// Requires a `granted` integration — see Service.ListFreeboxLanHosts.
	orgFreebox.GET("/:uid/lan-hosts", integrationsHandler.LanHostsHandler)

	// Status updates routes (authentication required)
	statusUpdatesService := statusupdates.NewService(s.dbService)
	// Fan published status updates out to confirmed status-page subscribers by
	// email. The notifier runs detached (fire-and-forget) inside the service.
	statusSubscriberNotifier := statussubscribers.NewNotifier(
		s.dbService, s.services.EmailSender, s.config.Server.BaseURL, slog.Default())
	statusUpdatesService.SetSubscriberNotifier(statusSubscriberNotifier)
	statusUpdatesHandler := statusupdates.NewHandler(statusUpdatesService, s.config)
	orgStatusUpdates := api.NewGroup("/orgs/:org/status-updates").Use(authMiddleware.RequireAuth)
	orgStatusUpdates.GET("", statusUpdatesHandler.ListStatusUpdates)
	orgStatusUpdates.POST("", statusUpdatesHandler.CreateStatusUpdate)
	orgStatusUpdates.GET("/:uid", statusUpdatesHandler.GetStatusUpdate)
	orgStatusUpdates.PATCH("/:uid", statusUpdatesHandler.UpdateStatusUpdate)
	orgStatusUpdates.DELETE("/:uid", statusUpdatesHandler.DeleteStatusUpdate)

	// Status pages routes (authentication required)
	statusPagesService := statuspages.NewService(s.dbService)
	statusPagesHandler := statuspages.NewHandler(statusPagesService, s.config)
	orgStatusPages := api.NewGroup("/orgs/:org/status-pages").Use(authMiddleware.RequireAuth)
	orgStatusPages.GET("", statusPagesHandler.ListStatusPages)
	orgStatusPages.POST("", statusPagesHandler.CreateStatusPage)
	orgStatusPages.GET("/:statusPageUid", statusPagesHandler.GetStatusPage)
	orgStatusPages.PATCH("/:statusPageUid", statusPagesHandler.UpdateStatusPage)
	orgStatusPages.DELETE("/:statusPageUid", statusPagesHandler.DeleteStatusPage)
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
		statusSubscribersService, s.dbService, s.services.EmailSender, s.config)
	// Authed admin: list (count + redactable addresses) and remove.
	orgStatusPages.GET("/:statusPageUid/subscribers", statusSubscribersHandler.ListSubscribers)
	orgStatusPages.DELETE("/:statusPageUid/subscribers/:uid", statusSubscribersHandler.RemoveSubscriber)

	// Maintenance windows routes (authentication required)
	mwService := maintenancewindows.NewService(s.dbService)
	mwHandler := maintenancewindows.NewHandler(mwService, s.config)
	orgMW := api.NewGroup("/orgs/:org/maintenance-windows").Use(authMiddleware.RequireAuth)
	orgMW.GET("", mwHandler.List)
	orgMW.POST("", mwHandler.Create)
	orgMW.GET("/:uid", mwHandler.Get)
	orgMW.PATCH("/:uid", mwHandler.Update)
	orgMW.DELETE("/:uid", mwHandler.Delete)
	orgMW.GET("/:uid/checks", mwHandler.ListChecks)
	orgMW.PUT("/:uid/checks", mwHandler.SetChecks)

	// Public status page endpoints (no authentication)
	api.GET("/status-pages/:org", statusPagesHandler.ViewDefaultStatusPage)
	api.GET("/status-pages/:org/:slug", statusPagesHandler.ViewStatusPage)
	// Public Atom/RSS feed of the status-update timeline.
	api.GET("/status-pages/:org/:slug/feed.xml", statusSubscribersHandler.Feed)

	// Public status-page subscription endpoints (no authentication). The
	// subscribe endpoint inherits the global per-IP rate limit on /api/v1/;
	// double opt-in is the primary anti-abuse control. Confirm/unsubscribe are
	// single-purpose token links that render an HTML landing page.
	api.POST("/orgs/:org/status-pages/:statusPageUid/subscribers", statusSubscribersHandler.Subscribe)
	publicSubscribers := api.NewGroup("/public/status-subscribers")
	publicSubscribers.GET("/confirm", statusSubscribersHandler.Confirm)
	publicSubscribers.GET("/unsubscribe", statusSubscribersHandler.Unsubscribe)

	// Slack integration routes (inbound from Slack - no org auth)
	slackService := slack.NewService(s.dbService, s.config, s.authService, checksService, incidentsService)
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

	// Slack destinations picker (authenticated, org-scoped)
	slackOrgRoutes := api.NewGroup("/orgs/:org/channels/:uid/slack").Use(authMiddleware.RequireAuth)
	slackOrgRoutes.GET("/destinations", slackHandler.GetDestinations)

	// Org-scoped install-URL minting (spec 2026-07-05-01): the org comes from
	// the authenticated route context (RequireOrgAccess), never from a query
	// param, so a workspace already connected to another org can be installed
	// again here without landing the user in — or joining them to — that
	// other org.
	slackOrgIntegrationRoutes := api.NewGroup("/orgs/:org/integrations/slack").
		Use(authMiddleware.RequireAuth, authMiddleware.RequireOrgAccess)
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

		mainGroup.GET(metricsPath, bunrouter.HTTPHandler(promhttp.Handler()))

		slog.InfoContext(ctx, "Prometheus metrics endpoint enabled", "path", metricsPath)
	}

	// Test API routes (no authentication for development/testing)
	testHandler := testapi.NewHandler(s.jobSvc, s.dbService, s.services.EventNotifier)
	api.POST("/test/jobs", testHandler.CreateEmailJob)
	api.GET("/fake", testHandler.FakeAPI)

	if s.config.RunMode == "test" {
		api.GET("/test/state-entries", testHandler.ListStateEntries)
		api.POST("/test/checks/bulk", testHandler.BulkCreateChecks)
		api.DELETE("/test/checks/bulk", testHandler.BulkDeleteChecks)
		api.POST("/test/generate-data", testHandler.GenerateData)
		api.DELETE("/test/checks/all", testHandler.DeleteAllChecks)
	}

	// OpenAPI schema + interactive (Swagger) explorer. The explorer moved from
	// /docs to /openapi now that /docs serves the documentation site.
	mainGroup.GET("/openapi.yaml", s.serveFile(openAPIFiles, "openapi/openapi.yaml"))
	mainGroup.GET("/openapi", s.serveFile(openAPIFiles, "openapi/index.html"))

	// Documentation site (Docusaurus), embedded and served at /docs on every
	// host. docs.solidping.io redirects its root here (see handlerWithDocsHost).
	mainGroup.GET("/docs", s.serveDocsRoute)
	mainGroup.GET("/docs/*path", s.serveDocsRoute)

	// Dash0 status page (served at /dash0/)
	mainGroup.GET("/dash0", s.serveDash0Root)
	mainGroup.GET("/dash0/*path", s.serveDash0Root)

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

func (s *Server) corsMiddleware(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(writer http.ResponseWriter, req bunrouter.Request) error {
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

func (s *Server) loggingMiddleware(next bunrouter.HandlerFunc) bunrouter.HandlerFunc {
	return func(w http.ResponseWriter, req bunrouter.Request) error {
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
}

// HealthNodeInfo contains node information for health response.
type HealthNodeInfo struct {
	Role   string `json:"role"`
	Region string `json:"region,omitempty"`
}

func (s *Server) healthCheck(writer http.ResponseWriter, _ bunrouter.Request) error {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(http.StatusOK)

	response := HealthResponse{
		Status: "ok",
		Node: &HealthNodeInfo{
			Role: s.config.Node.Role,
		},
	}

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
type LimitsResponse struct {
	RateLimit   LimitsRateLimit   `json:"rateLimit"`
	Concurrency LimitsConcurrency `json:"concurrency"`
}

func (s *Server) getLimits(writer http.ResponseWriter, req bunrouter.Request) error {
	cfg := s.rateLimiter.Config()
	state := s.rateLimiter.StateFor(s.rateLimiter.ExtractIP(req.Request))

	resp := LimitsResponse{
		RateLimit:   LimitsRateLimit{Enabled: cfg.RequestsPerMinute > 0},
		Concurrency: LimitsConcurrency{Enabled: cfg.MaxConcurrent > 0},
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

func (s *Server) getVersion(writer http.ResponseWriter, _ bunrouter.Request) error {
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

func (s *Server) serveFile(fs embed.FS, fileName string) func(writer http.ResponseWriter, _ bunrouter.Request) error {
	return func(writer http.ResponseWriter, _ bunrouter.Request) error {
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

// serveDocsRoute is the bunrouter handler for /docs and /docs/*path. It strips
// the /docs prefix and serves the matching file from the embedded docs build.
func (s *Server) serveDocsRoute(writer http.ResponseWriter, req bunrouter.Request) error {
	s.serveDocsFile(writer, strings.TrimPrefix(req.URL.Path, "/docs"))

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
func (s *Server) serveAppRoot(writer http.ResponseWriter, req bunrouter.Request) error {
	// Redirect root to dash0 dashboard
	if req.URL.Path == "/" {
		http.Redirect(writer, req.Request, "/dash0/", http.StatusFound)

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
	req bunrouter.Request,
	rule config.RedirectRule,
	fallback func(http.ResponseWriter, bunrouter.Request) error,
) error {
	// Build the new path by replacing the matched prefix with the target path
	newPath := rule.TargetPath + strings.TrimPrefix(req.URL.Path, rule.PathPrefix)

	slog.Debug("Proxying request",
		"originalPath", req.URL.Path,
		"targetHost", rule.TargetHost,
		"newPath", newPath,
	)

	//nolint:exhaustruct // Only Scheme and Host are needed for reverse proxy
	targetURL := &url.URL{
		Scheme: "http",
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

	proxy.ServeHTTP(writer, req.Request)

	return nil
}

// serveAppStatic serves static files from the embedded filesystem.
func (s *Server) serveAppStatic(writer http.ResponseWriter, req bunrouter.Request) error {
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
			slog.Error("Error reading file", "error", err)
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
func (s *Server) serveDash0Root(writer http.ResponseWriter, req bunrouter.Request) error {
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
func (s *Server) serveDash0Static(writer http.ResponseWriter, req bunrouter.Request) error {
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
			slog.Error("Error reading dash0 file", "error", err)
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
func (s *Server) serveStatus0Root(writer http.ResponseWriter, req bunrouter.Request) error {
	for i := range s.config.Server.Redirects {
		rule := &s.config.Server.Redirects[i]
		if strings.HasPrefix(req.URL.Path, rule.PathPrefix) {
			return s.serveAppRedirect(writer, req, *rule, s.serveStatus0Static)
		}
	}

	return s.serveStatus0Static(writer, req)
}

// serveStatus0Static serves static files from the embedded status0res filesystem.
func (s *Server) serveStatus0Static(writer http.ResponseWriter, req bunrouter.Request) error {
	reqPath := strings.TrimPrefix(req.URL.Path, "/status0")
	if reqPath == "" {
		reqPath = "/"
	}

	filePath := path.Join("status0res", reqPath)

	maxAgeSeconds := 31536000 // 1 year for assets

	data, err := status0Files.ReadFile(filePath)
	if err != nil {
		maxAgeSeconds = 60
		filePath = path.Join("status0res", "index.html")

		data, err = status0Files.ReadFile(filePath)
		if err != nil {
			slog.Error("Error reading status0 file", "error", err)
			http.Error(writer, "File not found", http.StatusNotFound)

			return nil
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

// Start starts the HTTP server and blocks until shutdown.
//
//nolint:funlen,cyclop // Server startup requires multiple conditional component initialization
func (s *Server) Start(ctx context.Context) error {
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
		slog.InfoContext(ctx, "Starting HTTP server", "listen", s.config.Server.Listen)

		const readHeaderTimeout = 10 * time.Second

		srv := &http.Server{
			Addr:              s.config.Server.Listen,
			Handler:           s.handlerWithDocsHost(),
			ReadHeaderTimeout: readHeaderTimeout,
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

		// Close the realtime hub first: it terminates every held-open
		// realtime WebSocket connection so srv.Shutdown (which waits for
		// active connections) can drain instead of hanging until the
		// shutdown timeout.
		if s.realtimeHub != nil {
			s.realtimeHub.Close()
		}

		// Shutdown HTTP server first to stop accepting new requests
		// Using fresh context for shutdown timeout after main ctx is canceled
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
		defer shutdownCancel()

		//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
		if err := srv.Shutdown(shutdownCtx); err != nil {
			//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
			slog.ErrorContext(shutdownCtx, "HTTP server shutdown error", "error", err)
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
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), s.config.Server.ShutdownTimeout)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		s.workersWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
		slog.InfoContext(shutdownCtx, "All runners stopped")
	case <-shutdownCtx.Done():
		//nolint:contextcheck // shutdownCtx intentionally separate for timeout management
		slog.WarnContext(shutdownCtx, "Timeout waiting for runners, forcing shutdown")
	}

	return ctx.Err()
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

		return nil
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
