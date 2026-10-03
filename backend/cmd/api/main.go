// Command api is the service entrypoint and the composition root.
//
// Every concrete dependency in the system is constructed here and nowhere
// else. That is what makes the dependency rule enforceable: the inner layers
// name only interfaces, and this one file is the single place that knows a
// PasswordHasher is bcrypt and a TicketRepository is Postgres. Swapping either
// is an edit to this file alone.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/blessnduta/ticketing-system/internal/adapter/eventbus"
	adapterhttp "github.com/blessnduta/ticketing-system/internal/adapter/http"
	"github.com/blessnduta/ticketing-system/internal/adapter/notifier"
	"github.com/blessnduta/ticketing-system/internal/adapter/postgres"
	"github.com/blessnduta/ticketing-system/internal/adapter/realtime"
	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
	"github.com/blessnduta/ticketing-system/internal/platform/config"
	"github.com/blessnduta/ticketing-system/internal/platform/database"
	"github.com/blessnduta/ticketing-system/internal/platform/logging"
	"github.com/blessnduta/ticketing-system/internal/platform/security"
)

func main() {
	// A distroless image has no shell and no curl, so the container health
	// check has to be the binary itself. `api -healthcheck` probes the local
	// readiness endpoint and exits 0 or 1 — a few lines here in exchange for
	// not shipping a shell into production.
	healthcheck := flag.Bool("healthcheck", false, "probe the local readiness endpoint and exit")
	flag.Parse()
	if *healthcheck {
		os.Exit(probeReadiness())
	}

	if err := run(); err != nil {
		// Writing to stderr rather than through the logger: a failure during
		// startup may well *be* a failure to configure the logger.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	logger := logging.New(cfg.LogLevel, cfg.LogFormat)
	logger.Info("starting ticketing-system",
		slog.String("env", cfg.Env),
		slog.String("port", cfg.Port))

	// Signal handling is installed before anything else so a Ctrl-C during a
	// slow migration is honoured rather than ignored.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- infrastructure -----------------------------------------------------
	pool, err := database.Connect(ctx, database.Config{
		URL:             cfg.DatabaseURL,
		MaxConns:        cfg.DBMaxConns,
		MinConns:        cfg.DBMinConns,
		MaxConnLifetime: cfg.DBMaxConnLife,
	}, logger)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer pool.Close()

	if err := pool.Migrate(ctx); err != nil {
		return fmt.Errorf("migrations: %w", err)
	}

	clock := shared.SystemClock{}
	hasher := security.NewBcryptHasher(cfg.BcryptCost)
	issuer := security.NewJWTIssuer(cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RealtimeTicketTTL)

	bus := eventbus.New(logger, 4)
	defer bus.Close()

	hub := realtime.NewHub(logger)

	// --- repositories -------------------------------------------------------
	orgRepo := postgres.NewOrganizationRepo(pool)
	userRepo := postgres.NewUserRepo(pool)
	teamRepo := postgres.NewTeamRepo(pool)
	ticketRepo := postgres.NewTicketRepo(pool)
	messageRepo := postgres.NewMessageRepo(pool)
	auditRepo := postgres.NewAuditRepo(pool)
	slaRepo := postgres.NewSLARepo(pool)
	assetRepo := postgres.NewAssetRepo(pool)
	approvalRepo := postgres.NewApprovalRepo(pool)
	tokenRepo := postgres.NewRefreshTokenRepo(pool)
	viewRepo := postgres.NewSavedViewRepo(pool)

	// --- use cases ----------------------------------------------------------
	slaService := app.NewSLAService(slaRepo, ticketRepo, userRepo, teamRepo, auditRepo, bus, clock, logger)
	authService := app.NewAuthService(pool, orgRepo, userRepo, teamRepo, slaRepo, tokenRepo,
		hasher, issuer, clock, cfg.RefreshTokenTTL)
	ticketService := app.NewTicketService(pool, ticketRepo, messageRepo, auditRepo, userRepo,
		teamRepo, orgRepo, assetRepo, approvalRepo, slaService, bus, clock)
	approvalService := app.NewApprovalService(pool, approvalRepo, ticketRepo, userRepo,
		auditRepo, bus, clock)
	assetService := app.NewAssetService(assetRepo, userRepo, clock)
	reportService := app.NewReportService(ticketRepo, clock)
	viewService := app.NewViewService(viewRepo, clock)

	// --- event subscribers --------------------------------------------------
	// Wiring subscribers here — rather than having services call the hub and
	// the notifier directly — is what keeps the ticket service unaware that
	// either exists. Adding a Slack integration is one more line in this
	// block and no change anywhere else.
	var mailer app.Notifier
	if cfg.SMTPHost != "" {
		mailer = notifier.NewSMTPNotifier(cfg.SMTPHost, cfg.SMTPPort, cfg.SMTPUser,
			cfg.SMTPPassword, cfg.SMTPFrom, logger)
		logger.Info("email notifications enabled", slog.String("smtp_host", cfg.SMTPHost))
	} else {
		mailer = notifier.NewLogNotifier(logger)
		logger.Warn("no SMTP configured — notifications will be logged, not sent")
	}

	mailSubscriber := notifier.NewSubscriber(mailer, userRepo, ticketRepo, cfg.PublicBaseURL, logger)
	bus.SubscribeAll(mailSubscriber.Handle)
	bus.SubscribeAll(func(_ context.Context, event app.Event) { hub.Broadcast(event) })

	// --- background workers -------------------------------------------------
	breachWorker := app.NewBreachWorker(slaService, ticketRepo, teamRepo, auditRepo, bus,
		clock, logger, cfg.SLASweepInterval)
	go breachWorker.Run(ctx)
	go pruneExpiredTokens(ctx, tokenRepo, clock, logger)

	// --- HTTP ---------------------------------------------------------------
	router := adapterhttp.NewRouter(adapterhttp.Dependencies{
		Config:    cfg,
		Logger:    logger,
		Auth:      authService,
		Tickets:   ticketService,
		Approvals: approvalService,
		Assets:    assetService,
		Reports:   reportService,
		Views:     viewService,
		SLA:       slaService,
		Users:     userRepo,
		Teams:     teamRepo,
		Issuer:    issuer,
		Hub:       hub,
		Clock:     clock,
		Healthy:   pool.Ping,
	})

	server := &http.Server{
		Addr:    ":" + cfg.Port,
		Handler: router,
		// Timeouts are set explicitly because Go's zero values mean "no
		// timeout", and a server with no read timeout can be held open
		// indefinitely by a slow client — Slowloris, with no effort required.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 16,
	}

	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", server.Addr))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- err
		}
	}()

	select {
	case err := <-serverErrors:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		logger.Info("shutdown signal received, draining connections")
	}

	// Graceful shutdown with its own context: ctx is already cancelled, and
	// reusing it would abort every in-flight request immediately — which is
	// precisely what graceful shutdown exists to avoid. An agent mid-save
	// gets to finish.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if err := server.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful shutdown failed, forcing close", slog.Any("error", err))
		_ = server.Close()
	}

	logger.Info("shutdown complete")
	return nil
}

// probeReadiness is the container health check. It returns a process exit
// code rather than an error: it is the whole of that invocation's behaviour.
func probeReadiness() int {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	response, err := client.Get("http://127.0.0.1:" + port + "/readyz")
	if err != nil {
		return 1
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

// pruneExpiredTokens deletes refresh tokens past their expiry.
//
// Without this the table grows by one row per login per device forever. Daily
// is frequent enough: expired tokens are already rejected at use, so this is
// housekeeping rather than a security control.
func pruneExpiredTokens(ctx context.Context, tokens app.RefreshTokenRepository,
	clock shared.Clock, logger *slog.Logger) {
	ticker := time.NewTicker(24 * time.Hour)
	defer ticker.Stop()

	prune := func() {
		removed, err := tokens.DeleteExpired(ctx, clock.Now())
		if err != nil {
			logger.ErrorContext(ctx, "token pruning failed", slog.Any("error", err))
			return
		}
		if removed > 0 {
			logger.InfoContext(ctx, "pruned expired refresh tokens", slog.Int64("count", removed))
		}
	}

	prune() // once at startup, so a long-running process is not the only trigger
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}
