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
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/getsentry/sentry-go"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/uptrace/bunrouter"

	"github.com/fclairamb/solidping/server/internal/app/services"
	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkworker"
	"github.com/fclairamb/solidping/server/internal/checkworker/checkjobsvc"
	"github.com/fclairamb/solidping/server/internal/config"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/postgres"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/handlers/auth"
	"github.com/fclairamb/solidping/server/internal/handlers/badges"
	"github.com/fclairamb/solidping/server/internal/handlers/checkconnections"
	"github.com/fclairamb/solidping/server/internal/handlers/checkgroups"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/handlers/checktypes"
	"github.com/fclairamb/solidping/server/internal/handlers/connections"
	"github.com/fclairamb/solidping/server/internal/handlers/events"
	"github.com/fclairamb/solidping/server/internal/handlers/heartbeat"
	"github.com/fclairamb/solidping/server/internal/handlers/incidents"
	"github.com/fclairamb/solidping/server/internal/handlers/jobs"
	"github.com/fclairamb/solidping/server/internal/handlers/maintenancewindows"
	"github.com/fclairamb/solidping/server/internal/handlers/members"
	regionshandler "github.com/fclairamb/solidping/server/internal/handlers/regions"
	"github.com/fclairamb/solidping/server/internal/handlers/results"
	"github.com/fclairamb/solidping/server/internal/handlers/statuspages"
	"github.com/fclairamb/solidping/server/internal/handlers/system"
	"github.com/fclairamb/solidping/server/internal/handlers/testapi"
	"github.com/fclairamb/solidping/server/internal/handlers/workers"
	"github.com/fclairamb/solidping/server/internal/integrations/slack"
	"github.com/fclairamb/solidping/server/internal/jobs/jobdef"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/jobs/jobtypes"
	"github.com/fclairamb/solidping/server/internal/jobs/jobworker"
	"github.com/fclairamb/solidping/server/internal/mcp"
	"github.com/fclairamb/solidping/server/internal/middleware"
	"github.com/fclairamb/solidping/server/internal/notifier"
	"github.com/fclairamb/solidping/server/internal/profiler"
	"github.com/fclairamb/solidping/server/internal/prommetrics"
	"github.com/fclairamb/solidping/server/internal/regions"
	"github.com/fclairamb/solidping/server/internal/systemconfig"
	"github.com/fclairamb/solidping/server/internal/version"
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

// Server is the HTTP server for the SolidPing application.
type Server struct {
	dbService   db.Service
	jobSvc      jobsvc.Service
	services    *services.Registry
	router      *bunrouter.Router
	config      *config.Config
	authService *auth.Service
	mcpHandler  *mcp.Handler
	profilerSrv *profiler.Server
	cancelCtx   context.CancelFunc
	workersWg   sync.WaitGroup // Tracks workers
}

// NewServer creates a new HTTP server instance.
//
//nolint:funlen,cyclop // Server setup requires multiple service initializations
func NewServer(ctx context.Context, cfg *config.Config) (*Server, error) {
	var (
		dbService db.Service
		err       error
	)

	// Initialize database service based on configuration

	switch cfg.Database.Type {
	case "postgres":
		dbService, err = postgres.New(ctx, postgres.Config{
			DSN:      cfg.Database.URL,
			Embedded: false,
			LogSQL:   cfg.Database.LogSQL,
			RunMode:  cfg.RunMode,
			Reset:    cfg.Database.Reset,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL service: %w", err)
		}
	case "postgres-embedded":
		dbService, err = postgres.New(ctx, postgres.Config{
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
	jobService := jobsvc.NewService(dbService.DB())
	svcList.Jobs = jobService

	checkJobService := checkjobsvc.NewService(dbService.DB())
	svcList.CheckJobs = checkJobService

	// Create check notifier based on database type
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

	// Create email services
	emailSender := email.NewSender(&cfg.Email, slog.Default())
	svcList.EmailSender = emailSender

	emailFormatter, err := email.NewFormatter()
	if err != nil {
		return nil, fmt.Errorf("failed to create email formatter: %w", err)
	}
	svcList.EmailFormatter = emailFormatter

	// Initialize Sentry error tracking
	if err := initSentry(cfg.Sentry); err != nil {
		return nil, fmt.Errorf("failed to initialize Sentry: %w", err)
	}

	// Create auth service
	authService := auth.NewService(dbService, cfg.Auth, cfg, emailSender, emailFormatter)

	server := &Server{
		dbService:   dbService,
		jobSvc:      jobService,
		services:    svcList,
		config:      cfg,
		authService: authService,
		profilerSrv: profiler.New(&cfg.Profiler),
	}

	server.setupRoutes()

	return server, nil
}

//nolint:funlen // Route registration function naturally grows with new routes
func (s *Server) setupRoutes() {
	router := bunrouter.New()
	mainGroup := router.Use(s.corsMiddleware).Use(middleware.SentryMiddleware()).Use(s.loggingMiddleware)

	// API routes
	api := mainGroup.NewGroup("/api/v1")
	api.OPTIONS("/*path", func(_ http.ResponseWriter, req bunrouter.Request) error {
		slog.InfoContext(req.Context(), "OPTIONS request", "path", req.URL.Path)
		return nil
	})

	// Create auth handler and middleware
	authHandler := auth.NewHandler(s.authService, s.config)
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

	// Slack OAuth routes (org-independent, public)
	if s.config.Slack.ClientID != "" {
		slackOAuthService := auth.NewSlackOAuthService(s.dbService, s.config, s.authService)
		slackOAuthHandler := auth.NewSlackOAuthHandler(slackOAuthService, s.config)
		slackAuth := api.NewGroup("/auth/slack")
		slackAuth.GET("/login", slackOAuthHandler.Login)
		slackAuth.GET("/callback", slackOAuthHandler.Callback)
	}

	// Google OAuth routes (org-scoped, public)
	if s.config.Google.ClientID != "" {
		googleOAuthService := auth.NewGoogleOAuthService(s.dbService, s.config, s.authService)
		googleOAuthHandler := auth.NewGoogleOAuthHandler(googleOAuthService, s.config)
		googleAuth := api.NewGroup("/auth/google")
		googleAuth.GET("/login", googleOAuthHandler.Login)
		googleAuth.GET("/callback", googleOAuthHandler.Callback)
	}

	// GitHub OAuth routes (org-scoped, public)
	if s.config.GitHub.ClientID != "" {
		gitHubOAuthService := auth.NewGitHubOAuthService(s.dbService, s.config, s.authService)
		gitHubOAuthHandler := auth.NewGitHubOAuthHandler(gitHubOAuthService, s.config)
		gitHubAuth := api.NewGroup("/auth/github")
		gitHubAuth.GET("/login", gitHubOAuthHandler.Login)
		gitHubAuth.GET("/callback", gitHubOAuthHandler.Callback)
	}

	// Microsoft OAuth routes (org-scoped, public)
	if s.config.Microsoft.ClientID != "" {
		microsoftOAuthService := auth.NewMicrosoftOAuthService(s.dbService, s.config, s.authService)
		microsoftOAuthHandler := auth.NewMicrosoftOAuthHandler(microsoftOAuthService, s.config)
		microsoftAuth := api.NewGroup("/auth/microsoft")
		microsoftAuth.GET("/login", microsoftOAuthHandler.Login)
		microsoftAuth.GET("/callback", microsoftOAuthHandler.Callback)
	}

	// GitLab OAuth routes (org-scoped, public)
	if s.config.GitLab.ClientID != "" {
		gitLabOAuthService := auth.NewGitLabOAuthService(s.dbService, s.config, s.authService)
		gitLabOAuthHandler := auth.NewGitLabOAuthHandler(gitLabOAuthService, s.config)
		gitLabAuth := api.NewGroup("/auth/gitlab")
		gitLabAuth.GET("/login", gitLabOAuthHandler.Login)
		gitLabAuth.GET("/callback", gitLabOAuthHandler.Callback)
	}

	// Discord OAuth routes (org-independent, public)
	if s.config.Discord.ClientID != "" {
		discordOAuthService := auth.NewDiscordOAuthService(s.dbService, s.config, s.authService)
		discordOAuthHandler := auth.NewDiscordOAuthHandler(discordOAuthService, s.config)
		discordAuth := api.NewGroup("/auth/discord")
		discordAuth.GET("/login", discordOAuthHandler.Login)
		discordAuth.GET("/callback", discordOAuthHandler.Callback)
	}

	// Auth providers endpoint (public)
	providersHandler := auth.NewProvidersHandler(s.config)
	api.GET("/auth/providers", providersHandler.ListProviders)

	// MCP endpoint (auth via PAT token, org derived from token)
	s.mcpHandler = mcp.NewHandler(s.dbService, s.services.EventNotifier, s.jobSvc)
	mcpGroup := api.NewGroup("/mcp").Use(authMiddleware.RequireAuth)
	mcpGroup.POST("", s.mcpHandler.Handle)

	// Job routes
	jobHandler := jobs.NewHandler(s.jobSvc)
	jobHandler.RegisterRoutes(api)

	// Check types routes
	activationResolver := checkerdef.NewActivationResolver(s.config.Checkers)
	checkTypesService := checktypes.NewService(activationResolver)
	checkTypesHandler := checktypes.NewHandler(checkTypesService, s.config)
	api.GET("/check-types", checkTypesHandler.ListServerCheckTypes) // Public, no auth
	orgCheckTypes := api.NewGroup("/orgs/:org/check-types").Use(authMiddleware.RequireAuth)
	orgCheckTypes.GET("", checkTypesHandler.ListOrgCheckTypes)

	// Check routes (authentication required)
	checksService := checks.NewService(s.dbService, s.services.EventNotifier)
	checksHandler := checks.NewHandler(checksService, s.config)
	orgChecks := api.NewGroup("/orgs/:org/checks").Use(authMiddleware.RequireAuth)
	orgChecks.GET("", checksHandler.ListChecks)
	orgChecks.GET("/export", checksHandler.ExportChecks)
	orgChecks.POST("/import", checksHandler.ImportChecks)
	orgChecks.POST("", checksHandler.CreateCheck)
	orgChecks.POST("/validate", checksHandler.ValidateCheck)
	orgChecks.GET("/:checkUid", checksHandler.GetCheck)
	orgChecks.PUT("/:slug", checksHandler.UpsertCheck)
	orgChecks.PATCH("/:checkUid", checksHandler.UpdateCheck)
	orgChecks.DELETE("/:checkUid", checksHandler.DeleteCheck)

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

	// Check-connection routes (authentication required)
	checkConnectionsService := checkconnections.NewService(s.dbService)
	checkConnectionsHandler := checkconnections.NewHandler(checkConnectionsService, s.config)
	orgChecks.GET("/:check/connections", checkConnectionsHandler.ListConnections)
	orgChecks.PUT("/:check/connections", checkConnectionsHandler.SetConnections)
	orgChecks.POST("/:check/connections/:connection", checkConnectionsHandler.AddConnection)
	orgChecks.DELETE("/:check/connections/:connection", checkConnectionsHandler.RemoveConnection)
	orgChecks.GET("/:check/connections/:connection", checkConnectionsHandler.GetConnectionSettings)
	orgChecks.PATCH("/:check/connections/:connection", checkConnectionsHandler.UpdateConnectionSettings)

	// Badge routes (public, no authentication required)
	badgesService := badges.NewService(s.dbService)
	badgesHandler := badges.NewHandler(badgesService, s.config)
	api.GET("/orgs/:org/checks/:check/badges/:format", badgesHandler.GetBadge)

	// Heartbeat ingestion routes (public, token-based auth)
	heartbeatService := heartbeat.NewService(s.dbService, s.jobSvc)
	heartbeatHandler := heartbeat.NewHandler(heartbeatService, s.config)
	api.POST("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)
	api.GET("/heartbeat/:org/:identifier", heartbeatHandler.ReceiveHeartbeat)

	// Edge worker API routes (worker token auth, no user auth)
	workersService := workers.NewService(
		s.dbService,
		s.services.CheckJobs,
		incidents.NewService(s.dbService, s.jobSvc),
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

	// Incidents routes (authentication required)
	incidentsService := incidents.NewService(s.dbService, s.jobSvc)
	incidentsHandler := incidents.NewHandler(incidentsService, s.config)
	orgIncidents := api.NewGroup("/orgs/:org/incidents").Use(authMiddleware.RequireAuth)
	orgIncidents.GET("", incidentsHandler.ListIncidents)
	orgIncidents.GET("/:uid", incidentsHandler.GetIncident)

	// Events routes (authentication required)
	eventsService := events.NewService(s.dbService)
	eventsHandler := events.NewHandler(eventsService, s.config)
	orgEvents := api.NewGroup("/orgs/:org/events").Use(authMiddleware.RequireAuth)
	orgEvents.GET("", eventsHandler.ListEvents)

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
	systemHandler := system.NewHandler(systemService, s.config)
	systemGroup := api.NewGroup("/system/parameters").
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireSuperAdmin)
	systemGroup.GET("", systemHandler.ListParameters)
	systemGroup.GET("/:key", systemHandler.GetParameter)
	systemGroup.PUT("/:key", systemHandler.SetParameter)
	systemGroup.DELETE("/:key", systemHandler.DeleteParameter)

	// System actions routes (super admin only)
	systemActions := api.NewGroup("/system").
		Use(authMiddleware.RequireAuth).
		Use(authMiddleware.RequireSuperAdmin)
	systemActions.POST("/test-email", systemHandler.TestEmail)

	// Integration connections routes (authentication required)
	connectionsService := connections.NewService(s.dbService)
	connectionsHandler := connections.NewHandler(connectionsService, s.config)
	orgConnections := api.NewGroup("/orgs/:org/connections").Use(authMiddleware.RequireAuth)
	orgConnections.GET("", connectionsHandler.ListConnections)
	orgConnections.POST("", connectionsHandler.CreateConnection)
	orgConnections.GET("/:uid", connectionsHandler.GetConnection)
	orgConnections.PATCH("/:uid", connectionsHandler.UpdateConnection)
	orgConnections.DELETE("/:uid", connectionsHandler.DeleteConnection)

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
	orgStatusPages.GET("/:statusPageUid/sections/:sectionUid", statusPagesHandler.GetSection)
	orgStatusPages.PATCH("/:statusPageUid/sections/:sectionUid", statusPagesHandler.UpdateSection)
	orgStatusPages.DELETE("/:statusPageUid/sections/:sectionUid", statusPagesHandler.DeleteSection)
	orgStatusPages.GET("/:statusPageUid/sections/:sectionUid/resources", statusPagesHandler.ListResources)
	orgStatusPages.POST("/:statusPageUid/sections/:sectionUid/resources", statusPagesHandler.CreateResource)
	orgStatusPages.PATCH("/:statusPageUid/sections/:sectionUid/resources/:resourceUid", statusPagesHandler.UpdateResource)
	orgStatusPages.DELETE("/:statusPageUid/sections/:sectionUid/resources/:resourceUid", statusPagesHandler.DeleteResource)

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

	// Slack integration routes (inbound from Slack - no org auth)
	slackService := slack.NewService(s.dbService, s.config, s.authService, checksService, incidentsService)
	slackHandler := slack.NewHandler(slackService, s.config)
	slackIntegration := api.NewGroup("/integrations/slack")
	slackIntegration.GET("/oauth", slackHandler.OAuthCallback)
	// Apply signature verification middleware to Slack webhooks
	slackIntegration.POST("/events", slackHandler.VerifyMiddleware(slackHandler.HandleEvents))
	slackIntegration.POST("/command", slackHandler.VerifyMiddleware(slackHandler.HandleCommand))
	slackIntegration.POST("/interaction", slackHandler.VerifyMiddleware(slackHandler.HandleInteraction))

	// Incident events (authentication required)
	orgIncidents.GET("/:uid/events", eventsHandler.ListIncidentEvents)

	// Check events (authentication required)
	orgChecks.GET("/:checkUid/events", eventsHandler.ListCheckEvents)

	// Management endpoints
	mgmt := mainGroup.NewGroup("/api/mgmt")
	mgmt.GET("/health", s.healthCheck)
	mgmt.GET("/version", s.getVersion)

	// Prometheus metrics endpoint
	if s.config.Prometheus.Enabled {
		prommetrics.Register(prometheus.DefaultRegisterer)

		metricsPath := s.config.Prometheus.Path
		if metricsPath == "" {
			metricsPath = "/metrics"
		}

		mainGroup.GET(metricsPath, bunrouter.HTTPHandler(promhttp.Handler()))

		slog.Info("Prometheus metrics endpoint enabled", "path", metricsPath)
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

	// OpenAPI schema endpoint
	mainGroup.GET("/openapi.yaml", s.serveFile(openAPIFiles, "openapi/openapi.yaml"))
	mainGroup.GET("/docs", s.serveFile(openAPIFiles, "openapi/index.html"))

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
	proxy := httputil.NewSingleHostReverseProxy(targetURL)

	// Modify the request to use the new path
	originalDirector := proxy.Director
	proxy.Director = func(r *http.Request) {
		originalDirector(r)
		r.URL.Path = newPath
		r.URL.RawPath = newPath
	}

	// When the dev server is unreachable, fall back to embedded static files
	if fallback != nil {
		proxy.ErrorHandler = func(writer http.ResponseWriter, _ *http.Request, err error) {
			slog.Warn("Dev server proxy failed, falling back to embedded files",
				"error", err,
				"targetHost", rule.TargetHost,
				"path", req.URL.Path,
			)

			if fbErr := fallback(writer, req); fbErr != nil {
				slog.Error("Fallback static serving failed", "error", fbErr)
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
			Handler:           s.router,
			ReadHeaderTimeout: readHeaderTimeout,
		}

		// Start HTTP server in a goroutine
		serverErr := make(chan error, 1)
		go func() {
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("HTTP server error", "error", err)
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

	// Update the server's auth config if JWT secret changed
	if cfg.Auth.JWTSecret != s.config.Auth.JWTSecret {
		// The auth service was already created with the old secret.
		// A restart is required for the new secret to take effect.
		slog.InfoContext(ctx, "JWT secret updated from system config, auth service will use new secret on restart")
	}

	return nil
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
