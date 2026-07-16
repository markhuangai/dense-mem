package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

var ErrUnsupportedPostgresTopology = errors.New("unsupported postgres topology")

type TopologyInfo struct {
	InRecovery             bool
	TransactionReadOnly    bool
	DistributedExtension   bool
	DistributedExtensionID string
}

func DetectTopology(ctx context.Context, db *gorm.DB) (TopologyInfo, error) {
	if db == nil {
		return TopologyInfo{}, errors.New("postgres topology: db is required")
	}
	var row struct {
		InRecovery             bool
		TransactionReadOnly    string
		DistributedExtension   bool
		DistributedExtensionID string
	}
	err := db.WithContext(ctx).Raw(`
		SELECT
			pg_is_in_recovery() AS in_recovery,
			current_setting('transaction_read_only') AS transaction_read_only,
			EXISTS (
				SELECT 1
				FROM pg_extension
				WHERE extname IN ('citus')
			) AS distributed_extension,
			COALESCE((
				SELECT extname
				FROM pg_extension
				WHERE extname IN ('citus')
				ORDER BY extname
				LIMIT 1
			), '') AS distributed_extension_id
	`).Scan(&row).Error
	if err != nil {
		return TopologyInfo{}, fmt.Errorf("postgres topology: detect failed: %w", err)
	}
	return TopologyInfo{
		InRecovery:             row.InRecovery,
		TransactionReadOnly:    strings.EqualFold(strings.TrimSpace(row.TransactionReadOnly), "on"),
		DistributedExtension:   row.DistributedExtension,
		DistributedExtensionID: row.DistributedExtensionID,
	}, nil
}

func ValidateSinglePrimaryTopology(ctx context.Context, db *gorm.DB) error {
	info, err := DetectTopology(ctx, db)
	if err != nil {
		return err
	}
	if info.InRecovery || info.TransactionReadOnly {
		return fmt.Errorf("%w: connected postgres is read-only or in recovery", ErrUnsupportedPostgresTopology)
	}
	if info.DistributedExtension {
		return fmt.Errorf("%w: detected distributed postgres extension %q", ErrUnsupportedPostgresTopology, info.DistributedExtensionID)
	}
	return nil
}
