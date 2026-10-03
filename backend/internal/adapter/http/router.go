package http

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/blessnduta/ticketing-system/internal/adapter/realtime"
	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/platform/config"
)

// Dependencies is the router's input. A single struct rather than fifteen
// positional parameters: adding a dependency then cannot silently shift an
// argument into the wrong slot, and every call site names what it is passing.
type Dependencies struct {
	Config config.Config
	Logger *slog.Logger

	Auth      *app.AuthService
	Tickets   *app.TicketService
	Approvals *app.ApprovalService
	Assets    *app.AssetService
	Reports   *app.ReportService
	Views     *app.ViewService
	SLA       *app.SLAService

	Users  app.UserRepository
	Teams  app.TeamRepository
	Issuer app.TokenIssuer
	Hub    *realtime.Hub

	Clock   sharedClock
	Healthy func(ctx context.Context) error
}

// sharedClock is aliased locally so this file does not need the shared import
// solely for a parameter type.
type sharedClock interface{ Now() time.Time }

// maxRequestBody bounds JSON payloads. Attachments do not pass through this
// API — they go straight to object storage via a presigned URL — so no
// legitimate request approaches this size.
const maxRequestBody = 1 << 20 // 1 MiB

// NewRouter builds the full route table.
//
// The table is written so that the security posture of any endpoint is
// readable from this one file: which group it sits in determines whether it is
// authenticated, and any RequirePermission is stated inline.
func NewRouter(deps Dependencies) *gin.Engine {
	if deps.Config.IsProduction() {
		gin.SetMode(gin.ReleaseMode)
	}

	router := gin.New()
	// Gin trusts X-Forwarded-For from every peer by default, which lets any
	// client spoof its IP and defeat IP-based rate limiting. Nil means "trust
	// nobody"; behind a load balancer this becomes that balancer's address.
	_ = router.SetTrustedProxies(nil)

	logger := deps.Logger

	router.Use(
		RequestID(),
		Recovery(logger),
		Logging(logger),
		SecurityHeaders(),
		CORS(deps.Config.CORSOrigins),
		MaxBodySize(maxRequestBody),
	)
	if deps.Config.IsProduction() {
		router.Use(HSTS())
	}

	generalLimiter := NewRateLimiter(deps.Config.RateLimitPerMinute, logger)
	// Authentication endpoints get a far tighter, IP-keyed limit: they are the
	// target of credential stuffing, and unlike the rest of the API there is
	// no authenticated identity to key on.
	authLimiter := NewRateLimiter(deps.Config.AuthRateLimitPerMinute, logger)

	authHandler := NewAuthHandler(deps.Auth, deps.Users, deps.Teams, logger)
	ticketHandler := NewTicketHandler(deps.Tickets, deps.Views, deps.Clock, logger)
	approvalHandler := NewApprovalHandler(deps.Approvals, logger)
	assetHandler := NewAssetHandler(deps.Assets, logger)
	reportHandler := NewReportHandler(deps.Reports, deps.SLA, logger)
	viewHandler := NewViewHandler(deps.Views, deps.Tickets, deps.Clock, logger)
	realtimeHandler := NewRealtimeHandler(deps.Hub, deps.Issuer, deps.Config.CORSOrigins, logger)

	// --- operational endpoints ---------------------------------------------
	// Liveness is deliberately dependency-free: if it checked the database, a
	// database blip would make the orchestrator kill healthy application
	// processes and turn a recoverable outage into a restart storm.
	router.GET("/healthz", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"status": "ok"})
	})
	// Readiness does check dependencies — that is the difference. A process
	// that cannot reach the database should stop receiving traffic without
	// being restarted.
	router.GET("/readyz", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
		defer cancel()
		if err := deps.Healthy(ctx); err != nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"status":               "ready",
			"realtime_connections": deps.Hub.ConnectionCount(),
		})
	})

	api := router.Group("/api/v1")

	// --- public ------------------------------------------------------------
	public := api.Group("")
	public.Use(authLimiter.ByIP())
	{
		public.POST("/auth/register-organization", authHandler.RegisterOrganization)
		public.POST("/auth/login", authHandler.Login)
		public.POST("/auth/refresh", authHandler.Refresh)
		public.POST("/auth/logout", authHandler.Logout)
	}

	// The WebSocket upgrade authenticates via its own short-lived ticket, so
	// it sits outside the bearer-token group.
	api.GET("/realtime", realtimeHandler.Connect)

	// --- authenticated ------------------------------------------------------
	authed := api.Group("")
	authed.Use(Authenticate(deps.Issuer, logger), generalLimiter.Middleware())
	{
		authed.GET("/me", authHandler.Me)
		authed.POST("/me/realtime-ticket", authHandler.RealtimeTicket)

		tickets := authed.Group("/tickets")
		{
			// Any authenticated user may create and list; the *scope* of what
			// they see is derived from their role inside the service.
			tickets.POST("", ticketHandler.Create)
			tickets.GET("", ticketHandler.List)
			tickets.GET("/:id", ticketHandler.Get)
			tickets.POST("/:id/messages", ticketHandler.AddMessage)

			tickets.PATCH("/:id/status", RequirePermission(identity.PermTicketTransition, logger), ticketHandler.Transition)
			tickets.PATCH("/:id/assignee", RequirePermission(identity.PermTicketAssign, logger), ticketHandler.Assign)
			tickets.PATCH("/:id/team", RequirePermission(identity.PermTicketAssign, logger), ticketHandler.Route)
			tickets.PATCH("/:id/classification", RequirePermission(identity.PermTicketTransition, logger), ticketHandler.Reclassify)

			tickets.PUT("/:id/assets/:assetId", RequirePermission(identity.PermTicketTransition, logger), ticketHandler.LinkAsset)
			tickets.DELETE("/:id/assets/:assetId", RequirePermission(identity.PermTicketTransition, logger), ticketHandler.UnlinkAsset)

			tickets.GET("/:id/audit", RequirePermission(identity.PermAuditRead, logger), ticketHandler.AuditTrail)
			tickets.POST("/:id/approvals", RequirePermission(identity.PermTicketTransition, logger), approvalHandler.Request)
		}
		// Registered outside the /tickets group because gin's router cannot
		// have both a wildcard `:id` and a literal `by-reference` segment at
		// the same position.
		authed.GET("/tickets-by-reference/:reference", ticketHandler.GetByReference)

		approvals := authed.Group("/approvals")
		approvals.Use(RequirePermission(identity.PermApprovalDecide, logger))
		{
			approvals.GET("/inbox", approvalHandler.Inbox)
			approvals.PATCH("/:id", approvalHandler.Decide)
		}

		assets := authed.Group("/assets")
		{
			assets.GET("", RequirePermission(identity.PermAssetRead, logger), assetHandler.List)
			assets.POST("", RequirePermission(identity.PermAssetManage, logger), assetHandler.Create)
			assets.GET("/hotspots", RequirePermission(identity.PermReportRead, logger), assetHandler.Hotspots)
		}

		views := authed.Group("/views")
		{
			views.GET("", viewHandler.List)
			views.POST("", viewHandler.Create)
			views.DELETE("/:id", viewHandler.Delete)
		}

		reports := authed.Group("/reports")
		reports.Use(RequirePermission(identity.PermReportRead, logger))
		{
			reports.GET("/overview", reportHandler.Overview)
		}

		slaGroup := authed.Group("/sla")
		slaGroup.Use(RequirePermission(identity.PermSLAManage, logger))
		{
			slaGroup.GET("", reportHandler.ListSLA)
			slaGroup.POST("/policies", reportHandler.CreatePolicy)
		}

		admin := authed.Group("/admin")
		{
			admin.GET("/users", RequirePermission(identity.PermTicketReadTeam, logger), authHandler.ListUsers)
			admin.POST("/users", RequirePermission(identity.PermUserManage, logger), authHandler.InviteUser)
			admin.GET("/teams", authHandler.ListTeams)
			admin.POST("/teams", RequirePermission(identity.PermTeamManage, logger), authHandler.CreateTeam)
		}
	}

	router.NoRoute(func(c *gin.Context) {
		badRequest(c, "route.not_found", "no such endpoint", nil)
	})

	return router
}
