// Package config loads and validates runtime configuration.
//
// Two rules it enforces, both learned the hard way:
//   * Validation happens at startup, not at first use. A missing database URL
//     should crash the process in the first second, not produce a 500 at 3am.
//   * Insecure defaults are refused outright in production rather than warned
//     about. A warning in a log nobody reads is not a control.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env  string
	Port string

	DatabaseURL     string
	DBMaxConns      int32
	DBMinConns      int32
	DBMaxConnLife   time.Duration

	JWTSecret       string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	RealtimeTicketTTL time.Duration

	// CORSOrigins is an explicit allowlist. There is no wildcard option: a
	// credentialed API that reflects arbitrary origins is exploitable from any
	// page the user visits.
	CORSOrigins []string

	BcryptCost int

	SLASweepInterval time.Duration

	SMTPHost     string
	SMTPPort     int
	SMTPUser     string
	SMTPPassword string
	SMTPFrom     string

	PublicBaseURL string

	LogLevel  string
	LogFormat string

	RateLimitPerMinute      int
	AuthRateLimitPerMinute  int
}

const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
	EnvTest        = "test"
)

func (c Config) IsProduction() bool { return c.Env == EnvProduction }

// Load reads .env (if present), then the environment, then validates.
// Real environment variables always win over the file, so a container's
// injected secrets are never shadowed by a stale .env left in the image.
func Load() (Config, error) {
	loadDotEnv(".env")

	cfg := Config{
		Env:               getString("ENV", EnvDevelopment),
		Port:              getString("PORT", "8080"),
		DatabaseURL:       getString("DATABASE_URL", ""),
		DBMaxConns:        int32(getInt("DB_MAX_CONNS", 25)),
		DBMinConns:        int32(getInt("DB_MIN_CONNS", 2)),
		DBMaxConnLife:     getDuration("DB_MAX_CONN_LIFETIME", time.Hour),
		JWTSecret:         getString("JWT_SECRET", ""),
		AccessTokenTTL:    getDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:   getDuration("REFRESH_TOKEN_TTL", 30*24*time.Hour),
		RealtimeTicketTTL: getDuration("REALTIME_TICKET_TTL", 30*time.Second),
		CORSOrigins:       getStringSlice("CORS_ORIGINS", []string{"http://localhost:5173"}),
		BcryptCost:        getInt("BCRYPT_COST", 12),
		SLASweepInterval:  getDuration("SLA_SWEEP_INTERVAL", time.Minute),
		SMTPHost:          getString("SMTP_HOST", ""),
		SMTPPort:          getInt("SMTP_PORT", 587),
		SMTPUser:          getString("SMTP_USER", ""),
		SMTPPassword:      getString("SMTP_PASSWORD", ""),
		SMTPFrom:          getString("SMTP_FROM", "servicedesk@example.com"),
		PublicBaseURL:     getString("PUBLIC_BASE_URL", "http://localhost:5173"),
		LogLevel:          getString("LOG_LEVEL", "info"),
		LogFormat:         getString("LOG_FORMAT", "text"),
		RateLimitPerMinute:     getInt("RATE_LIMIT_PER_MINUTE", 300),
		AuthRateLimitPerMinute: getInt("AUTH_RATE_LIMIT_PER_MINUTE", 10),
	}

	if cfg.Env == EnvDevelopment && cfg.DatabaseURL == "" {
		cfg.DatabaseURL = "postgres://ticketing:ticketing@localhost:5432/ticketing?sslmode=disable"
	}

	return cfg, cfg.validate()
}

// minJWTSecretLength is 32 bytes because HS256's security is bounded by the
// key, and a short secret is brute-forceable offline from a single captured
// token.
const minJWTSecretLength = 32

func (c Config) validate() error {
	var problems []string

	switch c.Env {
	case EnvDevelopment, EnvProduction, EnvTest:
	default:
		problems = append(problems, fmt.Sprintf("ENV must be development, production or test (got %q)", c.Env))
	}

	if c.DatabaseURL == "" {
		problems = append(problems, "DATABASE_URL is required")
	}

	if len(c.JWTSecret) < minJWTSecretLength {
		problems = append(problems, fmt.Sprintf(
			"JWT_SECRET must be at least %d characters (generate one with: openssl rand -base64 48)",
			minJWTSecretLength))
	}

	if c.BcryptCost < 10 || c.BcryptCost > 15 {
		problems = append(problems, "BCRYPT_COST must be between 10 and 15")
	}

	if c.AccessTokenTTL > time.Hour {
		problems = append(problems, "ACCESS_TOKEN_TTL must not exceed 1h — long-lived access tokens cannot be revoked")
	}

	if len(c.CORSOrigins) == 0 {
		problems = append(problems, "CORS_ORIGINS must list at least one origin")
	}
	for _, origin := range c.CORSOrigins {
		if origin == "*" {
			problems = append(problems, "CORS_ORIGINS must not contain '*' — this API sends credentials")
		}
	}

	if c.IsProduction() {
		if strings.Contains(c.DatabaseURL, "sslmode=disable") {
			problems = append(problems, "sslmode=disable is not permitted in production")
		}
		for _, origin := range c.CORSOrigins {
			if strings.HasPrefix(origin, "http://") && !strings.Contains(origin, "localhost") {
				problems = append(problems, fmt.Sprintf("CORS origin %q must use https in production", origin))
			}
		}
		if c.SMTPHost == "" {
			problems = append(problems, "SMTP_HOST is required in production — notifications are not optional for a ticketing system")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Readers
// ---------------------------------------------------------------------------

func getString(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func getInt(key string, fallback int) int {
	if raw, ok := os.LookupEnv(key); ok {
		if value, err := strconv.Atoi(raw); err == nil {
			return value
		}
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if raw, ok := os.LookupEnv(key); ok {
		if value, err := time.ParseDuration(raw); err == nil {
			return value
		}
	}
	return fallback
}

func getStringSlice(key string, fallback []string) []string {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return fallback
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// loadDotEnv is a deliberately small .env reader — enough for KEY=value with
// optional quotes and # comments, and no dependency. It never overwrites a
// variable that is already set.
func loadDotEnv(path string) {
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
}
