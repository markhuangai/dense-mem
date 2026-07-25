package repository

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestScanV2CompatibilityMarkerParsesMetadata(t *testing.T) {
	now := time.Date(2026, 7, 23, 17, 45, 0, 0, time.UTC)
	row := authorityScannerStub{
		values: []any{
			"marker-1",
			domain.V2MigrationMarkerKindCutover,
			"dense-mem.v2.1.cutover.v1",
			domain.V2MigrationMarkerCompatible,
			"",
			"sha256:corpus",
			"sha256:gates",
			`{"fresh_install":true}`,
			now,
		},
	}

	marker, err := scanV2CompatibilityMarker(row)

	require.NoError(t, err)
	require.Equal(t, "marker-1", marker.MarkerID)
	require.Equal(t, domain.V2MigrationMarkerCompatible, marker.Status)
	require.Equal(t, true, marker.Metadata["fresh_install"])
	require.Equal(t, now, marker.CreatedAt)
}

func TestScanV2CompatibilityMarkerReturnsScannerError(t *testing.T) {
	_, err := scanV2CompatibilityMarker(authorityScannerStub{err: sql.ErrNoRows})

	require.ErrorIs(t, err, sql.ErrNoRows)
}

func TestAuthorityJSONHelpersHandleEmptyInvalidAndUnsupportedValues(t *testing.T) {
	var metadata map[string]any
	require.NoError(t, unmarshalAuthorityJSON("", &metadata))
	require.Empty(t, metadata)

	require.Error(t, unmarshalAuthorityJSON("{", &metadata))

	_, err := marshalAuthorityJSON(map[string]any{"bad": func() {}})
	require.Error(t, err)
	require.ErrorContains(t, err, "encode json")
}

func TestFreshAuthorityApplicationTablesExcludeRetiredCleanupTables(t *testing.T) {
	retired := map[string]struct{}{
		"profiles":                 {},
		"api_keys":                 {},
		"memory_placement_runs":    {},
		"memory_placement_items":   {},
		"memory_dispute_sessions":  {},
		"community_detection_runs": {},
	}

	for _, table := range freshAuthorityApplicationTables {
		if _, ok := retired[table]; ok {
			t.Fatalf("fresh-install guard must not inspect retired cleanup table %q", table)
		}
	}
	require.Contains(t, freshAuthorityApplicationTables, "teams")
	require.Contains(t, freshAuthorityApplicationTables, "team_profiles")
	require.Contains(t, freshAuthorityApplicationTables, "v2_migration_runs")
}

type authorityScannerStub struct {
	values []any
	err    error
}

func (s authorityScannerStub) Scan(dest ...any) error {
	if s.err != nil {
		return s.err
	}
	if len(dest) != len(s.values) {
		return errors.New("destination/value length mismatch")
	}
	for i := range dest {
		switch target := dest[i].(type) {
		case *string:
			*target = s.values[i].(string)
		case *time.Time:
			*target = s.values[i].(time.Time)
		default:
			return errors.New("unsupported scan target")
		}
	}
	return nil
}
