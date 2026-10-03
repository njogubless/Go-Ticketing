package app

import (
	"context"
	"time"

	"github.com/blessnduta/ticketing-system/internal/domain/asset"
	"github.com/blessnduta/ticketing-system/internal/domain/identity"
	"github.com/blessnduta/ticketing-system/internal/domain/shared"
)

// AssetService manages the configuration-item inventory.
type AssetService struct {
	assets AssetRepository
	users  UserRepository
	clock  shared.Clock
}

func NewAssetService(assets AssetRepository, users UserRepository, clock shared.Clock) *AssetService {
	return &AssetService{assets: assets, users: users, clock: clock}
}

type CreateAssetInput struct {
	Tag           string
	Name          string
	Kind          asset.Kind
	Criticality   asset.Criticality
	Manufacturer  string
	Model         string
	SerialNumber  string
	Location      string
	OwnerID       *shared.ID
	ParentID      *shared.ID
	PurchasedAt   *time.Time
	WarrantyUntil *time.Time
}

func (s *AssetService) Create(ctx context.Context, actor identity.Actor, in CreateAssetInput) (*asset.Asset, error) {
	if err := actor.Require(identity.PermAssetManage); err != nil {
		return nil, err
	}

	if in.OwnerID != nil {
		if _, err := s.users.ByID(ctx, actor.OrgID, *in.OwnerID); err != nil {
			return nil, shared.Invalid("asset.owner_unknown", "that owner does not exist")
		}
	}
	if in.ParentID != nil {
		if _, err := s.assets.ByID(ctx, actor.OrgID, *in.ParentID); err != nil {
			return nil, shared.Invalid("asset.parent_unknown", "that parent asset does not exist")
		}
	}

	item, err := asset.New(asset.NewInput{
		OrgID:        actor.OrgID,
		Tag:          in.Tag,
		Name:         in.Name,
		Kind:         in.Kind,
		Criticality:  in.Criticality,
		Manufacturer: in.Manufacturer,
		Model:        in.Model,
		SerialNumber: in.SerialNumber,
		Location:     in.Location,
		OwnerID:      in.OwnerID,
		ParentID:     in.ParentID,
	}, s.clock.Now())
	if err != nil {
		return nil, err
	}
	item.PurchasedAt = in.PurchasedAt
	item.WarrantyUntil = in.WarrantyUntil

	if err := s.assets.Create(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// List returns the inventory. Agents may read it (they need to identify the
// laptop a caller is describing); only admins may change it.
func (s *AssetService) List(ctx context.Context, actor identity.Actor, filter AssetFilter) (shared.Page[*asset.Asset], error) {
	if err := actor.Require(identity.PermAssetRead); err != nil {
		return shared.Page[*asset.Asset]{}, err
	}
	return s.assets.List(ctx, actor.OrgID, filter)
}

func (s *AssetService) Get(ctx context.Context, actor identity.Actor, assetID shared.ID) (*asset.Asset, error) {
	if err := actor.Require(identity.PermAssetRead); err != nil {
		return nil, err
	}
	return s.assets.ByID(ctx, actor.OrgID, assetID)
}

// Assign hands an item to a person or returns it to stock.
func (s *AssetService) Assign(ctx context.Context, actor identity.Actor, assetID shared.ID, ownerID *shared.ID) (*asset.Asset, error) {
	if err := actor.Require(identity.PermAssetManage); err != nil {
		return nil, err
	}
	item, err := s.assets.ByID(ctx, actor.OrgID, assetID)
	if err != nil {
		return nil, err
	}
	if ownerID != nil {
		if _, err := s.users.ByID(ctx, actor.OrgID, *ownerID); err != nil {
			return nil, shared.Invalid("asset.owner_unknown", "that owner does not exist")
		}
	}
	if err := item.AssignTo(ownerID, s.clock.Now()); err != nil {
		return nil, err
	}
	if err := s.assets.Update(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

func (s *AssetService) Retire(ctx context.Context, actor identity.Actor, assetID shared.ID) (*asset.Asset, error) {
	if err := actor.Require(identity.PermAssetManage); err != nil {
		return nil, err
	}
	item, err := s.assets.ByID(ctx, actor.OrgID, assetID)
	if err != nil {
		return nil, err
	}
	item.Retire(s.clock.Now())
	if err := s.assets.Update(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// Hotspots ranks assets by incident count over a window.
//
// This is the payoff for linking tickets to assets at all: it converts a pile
// of individually-unremarkable tickets into "these six machines generated a
// quarter of this month's incidents", which is a purchasing conversation
// rather than a support one.
func (s *AssetService) Hotspots(ctx context.Context, actor identity.Actor, since time.Time, limit int) ([]AssetIncidentCount, error) {
	if err := actor.Require(identity.PermReportRead); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	return s.assets.IncidentCounts(ctx, actor.OrgID, since, limit)
}
