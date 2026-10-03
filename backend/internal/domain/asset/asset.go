// Package asset models configuration items — the equipment and services a
// ticket can be about.
//
// This is what separates an IT service desk from a generic helpdesk. Linking
// tickets to assets turns "another laptop problem" into "the fourth incident
// on this laptop model this month", which is the input to a problem record and
// eventually to a purchasing decision.
package asset

import (
	"strings"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

type ID = shared.ID

// Kind classifies a configuration item.
type Kind string

const (
	KindLaptop      Kind = "laptop"
	KindDesktop     Kind = "desktop"
	KindMobile      Kind = "mobile"
	KindServer      Kind = "server"
	KindNetworkGear Kind = "network"
	KindPrinter     Kind = "printer"
	KindSoftware    Kind = "software"
	KindService     Kind = "service" // a business service: payroll, VPN, email
	KindLicense     Kind = "license"
	KindOther       Kind = "other"
)

func ParseKind(raw string) (Kind, error) {
	switch Kind(raw) {
	case KindLaptop, KindDesktop, KindMobile, KindServer, KindNetworkGear,
		KindPrinter, KindSoftware, KindService, KindLicense, KindOther:
		return Kind(raw), nil
	default:
		return "", shared.Invalid("asset.kind_invalid", "unknown asset kind").WithDetail("kind", raw)
	}
}

// Criticality drives the default impact of incidents raised against the asset.
// A payroll server outage is organisation-wide before anyone assesses it; the
// asset already knows that.
type Criticality string

const (
	CriticalityLow    Criticality = "low"
	CriticalityMedium Criticality = "medium"
	CriticalityHigh   Criticality = "high"
)

func ParseCriticality(raw string) (Criticality, error) {
	switch Criticality(raw) {
	case CriticalityLow, CriticalityMedium, CriticalityHigh:
		return Criticality(raw), nil
	default:
		return "", shared.Invalid("asset.criticality_invalid", "criticality must be low, medium or high").
			WithDetail("criticality", raw)
	}
}

// Status is the lifecycle of the item itself, not of any ticket about it.
type Status string

const (
	StatusInStock     Status = "in_stock"
	StatusAssigned    Status = "assigned"
	StatusMaintenance Status = "maintenance"
	StatusRetired     Status = "retired"
)

func ParseStatus(raw string) (Status, error) {
	switch Status(raw) {
	case StatusInStock, StatusAssigned, StatusMaintenance, StatusRetired:
		return Status(raw), nil
	default:
		return "", shared.Invalid("asset.status_invalid", "unknown asset status").WithDetail("status", raw)
	}
}

// Asset is a configuration item.
type Asset struct {
	ID    ID
	OrgID ID
	// Tag is the organisation's own inventory label — the sticker on the
	// laptop. Unique per organisation, and how a requester will identify it.
	Tag         string
	Name        string
	Kind        Kind
	Status      Status
	Criticality Criticality

	Manufacturer string
	Model        string
	SerialNumber string
	Location     string

	// OwnerID is the person currently accountable for the item. Nil for
	// shared infrastructure.
	OwnerID *ID
	// ParentID models dependency: a business service depends on the servers
	// beneath it. Walking parents is how an incident on one host explains
	// itself as an outage of the service above it.
	ParentID *ID

	PurchasedAt   *time.Time
	WarrantyUntil *time.Time

	CreatedAt time.Time
	UpdatedAt time.Time
}

const (
	maxTagLength  = 60
	maxNameLength = 160
	maxFreeText   = 120
)

// NewInput is the validated construction payload.
type NewInput struct {
	OrgID        ID
	Tag          string
	Name         string
	Kind         Kind
	Criticality  Criticality
	Manufacturer string
	Model        string
	SerialNumber string
	Location     string
	OwnerID      *ID
	ParentID     *ID
}

func New(in NewInput, now time.Time) (*Asset, error) {
	tag := strings.ToUpper(strings.TrimSpace(in.Tag))
	if tag == "" {
		return nil, shared.Invalid("asset.tag_required", "an asset tag is required")
	}
	if len(tag) > maxTagLength {
		return nil, shared.Invalid("asset.tag_too_long", "asset tag is too long")
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		return nil, shared.Invalid("asset.name_required", "an asset name is required")
	}
	if len(name) > maxNameLength {
		return nil, shared.Invalid("asset.name_too_long", "asset name is too long")
	}
	for label, value := range map[string]string{
		"manufacturer":  in.Manufacturer,
		"model":         in.Model,
		"serial_number": in.SerialNumber,
		"location":      in.Location,
	} {
		if len(value) > maxFreeText {
			return nil, shared.Invalid("asset.field_too_long", "field is too long").WithDetail("field", label)
		}
	}

	status := StatusInStock
	if in.OwnerID != nil {
		status = StatusAssigned
	}

	return &Asset{
		ID:           shared.NewID(),
		OrgID:        in.OrgID,
		Tag:          tag,
		Name:         name,
		Kind:         in.Kind,
		Status:       status,
		Criticality:  in.Criticality,
		Manufacturer: strings.TrimSpace(in.Manufacturer),
		Model:        strings.TrimSpace(in.Model),
		SerialNumber: strings.TrimSpace(in.SerialNumber),
		Location:     strings.TrimSpace(in.Location),
		OwnerID:      in.OwnerID,
		ParentID:     in.ParentID,
		CreatedAt:    now,
		UpdatedAt:    now,
	}, nil
}

// AssignTo hands the item to a person, or returns it to stock when nil.
func (a *Asset) AssignTo(ownerID *ID, now time.Time) error {
	if a.Status == StatusRetired {
		return shared.RuleViolation("asset.retired", "a retired asset cannot be assigned")
	}
	a.OwnerID = ownerID
	if ownerID == nil {
		a.Status = StatusInStock
	} else {
		a.Status = StatusAssigned
	}
	a.UpdatedAt = now
	return nil
}

// Retire removes the item from service. Retired assets stay in the database:
// tickets that referenced them must remain readable.
func (a *Asset) Retire(now time.Time) {
	a.Status = StatusRetired
	a.OwnerID = nil
	a.UpdatedAt = now
}

// SuggestedImpact maps criticality onto a ticket's default impact. The service
// desk can always override it; this only sets the starting point, so that a
// requester who has no idea what "impact" means still produces a sane triage.
func (a *Asset) SuggestedImpact() string {
	switch a.Criticality {
	case CriticalityHigh:
		return "high"
	case CriticalityMedium:
		return "medium"
	default:
		return "low"
	}
}

// UnderWarranty reports whether a repair should go to the vendor rather than
// to the desk's own budget.
func (a *Asset) UnderWarranty(at time.Time) bool {
	return a.WarrantyUntil != nil && at.Before(*a.WarrantyUntil)
}
