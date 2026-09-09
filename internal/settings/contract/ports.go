package contract

import (
	"context"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// AppConfigRepository is the persistence boundary for system application
// settings. The settings application owns normalization and effective values.
type AppConfigRepository interface {
	GetUpdateTime(context.Context) (string, error)
	List(context.Context) (map[string]domain.AppConfigEntry, error)
	UpdateValues(context.Context, map[string]string, string, time.Time) (bool, error)
}

// SecurityRepository persists global security settings, failure counters, and
// normalized IP bans. Security policy remains in the settings application.
type SecurityRepository interface {
	GetSettings(context.Context) (*domain.SecuritySettings, error)
	UpdateSettings(context.Context, domain.SecuritySettings) (*domain.SecuritySettings, error)
	GetActiveBan(context.Context, string, time.Time) (*domain.SecurityIPBan, error)
	RecordFailure(context.Context, string, string, string, int, time.Time) (*domain.SecurityIPFailure, error)
	UpsertBan(context.Context, *domain.SecurityIPBan) error
	ListBans(context.Context, bool, int, int) ([]domain.SecurityIPBan, int64, error)
	DeleteBan(context.Context, string, time.Time) error
}
