package store

import (
	"context"

	"github.com/pwshehan/device-status-monitor/internal/model"
)

// Resolver holds the two lower tiers of the effective-value chain — the global
// defaults and every group — so many devices can be resolved without querying
// per device.
//
// It exists because the API renders inheritance for devices the engine is not
// running (disabled, paused, just created), so it cannot read the scheduler's
// in-memory set. Both paths still end in model.Resolve, which stays the only
// place precedence is decided.
type Resolver struct {
	defaults model.Defaults
	groups   map[int64]*model.Group
}

// NewResolver reads the defaults and the group table once.
func (s *Store) NewResolver(ctx context.Context) (*Resolver, error) {
	defaults, err := s.Defaults(ctx)
	if err != nil {
		return nil, err
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]*model.Group, len(groups))
	for i := range groups {
		byID[groups[i].ID] = &groups[i]
	}
	return &Resolver{defaults: defaults, groups: byID}, nil
}

// Resolve folds one device with its group and the global defaults.
func (r *Resolver) Resolve(d model.Device) model.Effective {
	return model.Resolve(d, r.Group(d.GroupID), r.defaults)
}

// Group returns the group a device belongs to, or nil when ungrouped or when
// the id no longer exists.
func (r *Resolver) Group(id *int64) *model.Group {
	if id == nil {
		return nil
	}
	return r.groups[*id]
}

// Defaults returns the global tier.
func (r *Resolver) Defaults() model.Defaults { return r.defaults }
