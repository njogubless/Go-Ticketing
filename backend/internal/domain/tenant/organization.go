// Package tenant models the organisation — the isolation boundary every other
// entity hangs from. Nothing in this system exists outside an organisation.
package tenant

import (
	"regexp"
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// Organization is the tenant. Its ID appears on every table and in every
// query; see internal/adapter/postgres for how that is enforced rather than
// merely intended.
type Organization struct {
	ID   shared.ID
	Name string
	// Slug is the URL-safe handle used for tenant-scoped public routes and
	// for the login form's "which company?" step.
	Slug string
	// Timezone is the IANA name (e.g. "Africa/Nairobi") used as the default
	// business calendar for SLA arithmetic. Storing a name rather than a UTC
	// offset is essential: offsets change twice a year in DST regions, and an
	// SLA computed against a stale offset silently drifts by an hour.
	Timezone string
	// TicketPrefix is prepended to the human-facing ticket reference,
	// e.g. "ACME" gives ACME-1042. Agents quote these on calls; a UUID is
	// unusable for that.
	TicketPrefix string
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

var (
	slugPattern   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{1,38}[a-z0-9])$`)
	prefixPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{1,7}$`)
)

// NewOrganization validates and constructs a tenant.
func NewOrganization(name, slug, prefix, timezone string, now time.Time) (*Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, shared.Invalid("org.name_required", "organisation name is required")
	}
	if len(name) > 120 {
		return nil, shared.Invalid("org.name_too_long", "organisation name is too long")
	}

	slug = strings.ToLower(strings.TrimSpace(slug))
	if !slugPattern.MatchString(slug) {
		return nil, shared.Invalid("org.slug_invalid",
			"slug must be 3-40 lowercase letters, digits or hyphens, and start and end with a letter or digit")
	}
	if reservedSlugs[slug] {
		return nil, shared.Conflict("org.slug_reserved", "that slug is reserved").WithDetail("slug", slug)
	}

	prefix = strings.ToUpper(strings.TrimSpace(prefix))
	if !prefixPattern.MatchString(prefix) {
		return nil, shared.Invalid("org.prefix_invalid",
			"ticket prefix must be 2-8 uppercase letters or digits, starting with a letter")
	}

	if err := ValidateTimezone(timezone); err != nil {
		return nil, err
	}

	return &Organization{
		ID:           shared.NewID(),
		Name:         name,
		Slug:         slug,
		Timezone:     timezone,
		TicketPrefix: prefix,
		Active:       true,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// ValidateTimezone checks the name against the system tz database. Rejecting
// unknown zones here means the SLA calculator can load a location without
// error handling on every call.
func ValidateTimezone(name string) error {
	if strings.TrimSpace(name) == "" {
		return shared.Invalid("org.timezone_required", "timezone is required")
	}
	if _, err := time.LoadLocation(name); err != nil {
		return shared.Invalid("org.timezone_invalid", "unknown IANA timezone").WithDetail("timezone", name)
	}
	return nil
}

// reservedSlugs prevents tenants claiming handles that collide with system
// routes or that could be used to impersonate the platform itself.
var reservedSlugs = map[string]bool{
	"admin": true, "api": true, "app": true, "www": true, "status": true,
	"support": true, "help": true, "login": true, "signup": true, "auth": true,
	"static": true, "assets": true, "public": true, "internal": true, "system": true,
}
