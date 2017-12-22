package memory

import (
	"database/sql"
	"time"

	"github.com/weaveworks/cortex/pkg/configs"
)

type config struct {
	cfg       configs.Config
	id        configs.ID
	deletedAt time.Time
}

func (c config) toView() configs.View {
	return configs.View{
		ID:     c.id,
		Config: c.cfg,
	}
}

// DB is an in-memory database for testing, and local development
type DB struct {
	cfgs map[string]config
	id   uint
}

// New creates a new in-memory database
func New(_, _ string) (*DB, error) {
	return &DB{
		cfgs: map[string]config{},
		id:   0,
	}, nil
}

func isActive(c config) bool {
	return c.deletedAt.IsZero()
}

// GetConfig gets the user's configuration.
func (d *DB) GetConfig(userID string) (configs.View, error) {
	c, ok := d.cfgs[userID]
	if !ok {
		return configs.View{}, sql.ErrNoRows
	}
	if !isActive(c) {
		return configs.View{}, sql.ErrNoRows
	}
	return c.toView(), nil
}

// SetConfig sets configuration for a user.
func (d *DB) SetConfig(userID string, cfg configs.Config) error {
	d.cfgs[userID] = config{cfg: cfg, id: configs.ID(d.id)}
	d.id++
	return nil
}

// GetAllConfigs gets all of the configs.
func (d *DB) GetAllConfigs() (map[string]configs.View, error) {
	cfgs := map[string]configs.View{}
	for user, c := range d.cfgs {
		if isActive(c) {
			cfgs[user] = c.toView()
		}
	}
	return cfgs, nil
}

// GetConfigs gets all of the configs that have changed recently.
func (d *DB) GetConfigs(since configs.ID) (map[string]configs.View, error) {
	cfgs := map[string]configs.View{}
	for user, c := range d.cfgs {
		if c.id > since && isActive(c) {
			cfgs[user] = c.toView()
		}
	}
	return cfgs, nil
}

// DeactivateConfig deactivates configuration for a user.
func (d *DB) DeactivateConfig(userID string) error {
	cfg, ok := d.cfgs[userID]
	if !ok {
		return sql.ErrNoRows
	}
	cfg.deletedAt = time.Now()
	return nil
}

// RestoreConfig restores deactivated configuration for a user.
func (d *DB) RestoreConfig(userID string) error {
	cfg, ok := d.cfgs[userID]
	if !ok {
		return sql.ErrNoRows
	}
	cfg.deletedAt = time.Time{}
	return nil
}

// Close finishes using the db. Noop.
func (d *DB) Close() error {
	return nil
}
