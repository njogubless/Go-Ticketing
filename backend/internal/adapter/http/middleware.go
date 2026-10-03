package http

import (
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"

	"github.com/blessnduta/ticketing-system/internal/app"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

const (
	ctxActorKey     = "actor"
	ctxRequestIDKey = "request_id"
)

// ---------------------------------------------------------------------------
// Request identity
// ---------------------------------------------------------------------------

// RequestID assigns every request a correlation ID and echoes it back.
//
// A client-supplied X-Request-ID is honoured — that is how a trace spans a
// frontend and a backend — but it is length-capped and stripped of control
// characters, because it ends up in log lines and a newline there is log
// injection.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		requestID := sanitiseRequestID(c.GetHeader("X-Request-ID"))
		if requestID == "" {
			requestID = shared.NewID().String()
		}
		c.Set(ctxRequestIDKey, requestID)
		c.Header("X-Request-ID", requestID)
		c.Next()
	}
}

func sanitiseRequestID(raw string) string {
	if len(raw) > 64 {
		raw = raw[:64]
	}
	var builder strings.Builder
	for _, r := range raw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' {
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func requestIDFrom(c *gin.Context) string {
	if value, ok := c.Get(ctxRequestIDKey); ok {
		if requestID, ok := value.(string); ok {
			return requestID
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Logging and recovery
// ---------------------------------------------------------------------------

// Logging emits one structured line per request.
func Logging(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()

		attrs := []any{
			slog.String("request_id", requestIDFrom(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", c.FullPath()),
			slog.Int("status", c.Writer.Status()),
			slog.Duration("duration", time.Since(start)),
		}
		// The acting user, when known, is what makes an access log answer
		// "who did this?" — the question that actually gets asked.
		if actor, ok := ActorFrom(c); ok {
			attrs = append(attrs,
				slog.String("user_id", actor.UserID.String()),
				slog.String("org_id", actor.OrgID.String()))
		}

		switch {
		case c.Writer.Status() >= 500:
			logger.ErrorContext(c.Request.Context(), "request", attrs...)
		case c.Writer.Status() >= 400:
			logger.WarnContext(c.Request.Context(), "request", attrs...)
		default:
			logger.InfoContext(c.Request.Context(), "request", attrs...)
		}
	}
}

// Recovery converts a panic into a 500 without killing the process.
func Recovery(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if recovered := recover(); recovered != nil {
				logger.ErrorContext(c.Request.Context(), "panic recovered",
					slog.String("request_id", requestIDFrom(c)),
					slog.String("path", c.FullPath()),
					slog.Any("panic", recovered))
				respondError(c, logger, shared.Internal("internal_error", "something went wrong on our end"))
			}
		}()
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Security headers and CORS
// ---------------------------------------------------------------------------

// SecurityHeaders sets defensive response headers.
//
// This API returns JSON only and is consumed by a separate SPA, so the CSP can
// be maximally restrictive: nothing should ever load a resource from an API
// response. The headers still matter because a browser navigated directly to
// an endpoint will render the response, and a reflected value in a JSON body
// has been an XSS vector before.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "DENY")
		c.Header("Referrer-Policy", "no-referrer")
		c.Header("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		c.Header("Permissions-Policy", "geolocation=(), microphone=(), camera=()")
		// Cache-Control on an authenticated API prevents a shared proxy from
		// serving one user's ticket list to the next user through it.
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

// HSTS instructs browsers to refuse plaintext. Only meaningful over TLS, so it
// is applied conditionally in production.
func HSTS() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		c.Next()
	}
}

// CORS implements an explicit-allowlist policy.
//
// The origin is matched against the configured list and echoed back verbatim
// only on a match. Reflecting whatever the caller sent — the "just make CORS
// work" fix — combined with Allow-Credentials means any website the user visits
// can read their tickets. There is no wildcard branch here on purpose.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		allowed[strings.TrimRight(origin, "/")] = true
	}

	return func(c *gin.Context) {
		origin := strings.TrimRight(c.GetHeader("Origin"), "/")
		if origin != "" && allowed[origin] {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
			c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
			c.Header("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID, If-Unmodified-Since")
			c.Header("Access-Control-Expose-Headers", "X-Request-ID, Last-Modified")
			c.Header("Access-Control-Max-Age", "600")
			// Vary is required whenever the response depends on the request's
			// Origin, or a cache will serve one origin's headers to another.
			c.Header("Vary", "Origin")
		}

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Authentication
// ---------------------------------------------------------------------------

// Authenticate validates the bearer token and attaches the actor.
//
// Everything downstream reads identity from the context, never from the
// request. That is the property that makes horizontal privilege escalation
// impossible by construction: there is no user ID or organisation ID in a
// request body that any handler consults to decide who is acting.
func Authenticate(issuer app.TokenIssuer, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		token, found := strings.CutPrefix(header, "Bearer ")
		if !found || strings.TrimSpace(token) == "" {
			respondError(c, logger, shared.Unauthorized("auth.required",
				"authentication required"))
			return
		}

		actor, err := issuer.Parse(strings.TrimSpace(token))
		if err != nil {
			respondError(c, logger, err)
			return
		}
		if actor.IsZero() {
			respondError(c, logger, shared.Unauthorized("auth.invalid_token", "invalid token"))
			return
		}

		c.Set(ctxActorKey, actor)
		c.Next()
	}
}

// ActorFrom reads the authenticated actor from the context.
func ActorFrom(c *gin.Context) (identity.Actor, bool) {
	value, exists := c.Get(ctxActorKey)
	if !exists {
		return identity.Actor{}, false
	}
	actor, ok := value.(identity.Actor)
	return actor, ok
}

// mustActor is used inside authenticated routes. A missing actor there means
// the route was registered without Authenticate — a wiring bug that must fail
// loudly and immediately rather than silently proceeding with a zero actor.
func mustActor(c *gin.Context, logger *slog.Logger) (identity.Actor, bool) {
	actor, ok := ActorFrom(c)
	if !ok {
		logger.ErrorContext(c.Request.Context(),
			"authenticated route reached without an actor — check route wiring",
			slog.String("path", c.FullPath()))
		respondError(c, logger, shared.Unauthorized("auth.required", "authentication required"))
		return identity.Actor{}, false
	}
	return actor, true
}

// RequirePermission guards a route. Services check permissions too; this is
// defence in depth and, just as usefully, documentation — the route table
// shows what each endpoint needs.
func RequirePermission(permission identity.Permission, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		actor, ok := mustActor(c, logger)
		if !ok {
			return
		}
		if err := actor.Require(permission); err != nil {
			respondError(c, logger, err)
			return
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Rate limiting
// ---------------------------------------------------------------------------

// RateLimiter is a per-key token bucket held in memory.
//
// In-memory, and therefore per-instance: behind three replicas the effective
// limit is three times the configured one. That is an acceptable approximation
// for abuse control and a bad one for billing or quotas — the ADR records that
// a shared Redis limiter is the upgrade path. What it does buy, cheaply, is
// that credential stuffing against the login endpoint stops being free.
type RateLimiter struct {
	mu       sync.Mutex
	buckets  map[string]*bucket
	rate     rate.Limit
	burst    int
	logger   *slog.Logger
}

type bucket struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func NewRateLimiter(perMinute int, logger *slog.Logger) *RateLimiter {
	limiter := &RateLimiter{
		buckets: make(map[string]*bucket),
		rate:    rate.Limit(float64(perMinute) / 60.0),
		// Burst equal to a tenth of the minute allowance lets a page that
		// fires several requests on load through, while still throttling a
		// sustained flood.
		burst:  max(perMinute/10, 5),
		logger: logger,
	}
	go limiter.evictIdle()
	return limiter
}

// evictIdle prevents the bucket map from growing without bound — one entry per
// distinct IP, forever, is a slow memory leak that only shows up in production.
func (l *RateLimiter) evictIdle() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		cutoff := time.Now().Add(-15 * time.Minute)
		l.mu.Lock()
		for key, b := range l.buckets {
			if b.lastSeen.Before(cutoff) {
				delete(l.buckets, key)
			}
		}
		l.mu.Unlock()
	}
}

func (l *RateLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	b, exists := l.buckets[key]
	if !exists {
		b = &bucket{limiter: rate.NewLimiter(l.rate, l.burst)}
		l.buckets[key] = b
	}
	b.lastSeen = time.Now()
	return b.limiter.Allow()
}

// Middleware limits by authenticated user when available, falling back to
// client IP. Keying on the user first matters because a whole office behind one
// NAT would otherwise share — and exhaust — a single IP bucket.
func (l *RateLimiter) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.ClientIP()
		if actor, ok := ActorFrom(c); ok {
			key = "user:" + actor.UserID.String()
		}
		if !l.allow(key) {
			respondError(c, l.logger, shared.RateLimited("rate.limited",
				"too many requests — please slow down"))
			return
		}
		c.Next()
	}
}

// ByIP limits strictly by IP, for unauthenticated endpoints (login, register)
// where there is no user to key on and abuse is most likely.
func (l *RateLimiter) ByIP() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !l.allow("ip:" + c.ClientIP()) {
			respondError(c, l.logger, shared.RateLimited("rate.limited",
				"too many attempts — please wait a moment before trying again"))
			return
		}
		c.Next()
	}
}

// ---------------------------------------------------------------------------
// Body limits
// ---------------------------------------------------------------------------

// MaxBodySize caps request bodies. Without it, a single client can stream an
// unbounded body and exhaust memory before any handler has a chance to reject
// it — the JSON decoder would happily buffer all of it first.
func MaxBodySize(limit int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, limit)
		c.Next()
	}
}
