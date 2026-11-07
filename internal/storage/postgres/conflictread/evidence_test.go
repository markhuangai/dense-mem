package conflictread

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	conflictcontract "github.com/markhuangai/dense-mem/internal/conflict/contract"
	"github.com/stretchr/testify/require"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestEvidenceConflictReaders(t *testing.T) {
	const teamID = "00000000-0000-4000-8000-000000000001"
	const conflictID = "00000000-0000-4000-8000-000000000002"
	at := time.Date(2026, 11, 6, 12, 0, 0, 123000, time.UTC)
	want := conflictcontract.EvidenceConflictPositionRecord{
		ConflictID: conflictID, PositionID: "position", PositionKey: "a",
		CanonicalEvidenceID: "canonical", CanonicalOwnerProfileID: "canonical-owner",
		OccurrenceID: "occurrence", OccurrenceOwnerProfileID: "occurrence-owner",
		Quote: "Élan", SpanStart: 2, SpanEnd: 6, Authority: "secondary", Submitted: true, CreatedAt: at,
	}
	positionRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"conflict_id", "position_id", "position_key", "canonical_evidence_id", "canonical_owner_profile_id", "occurrence_id", "occurrence_owner_profile_id", "quote", "span_start", "span_end", "authority", "submitted", "created_at"})
	}
	addPosition := func(rows *sqlmock.Rows, spanStart driver.Value) *sqlmock.Rows {
		return rows.AddRow(conflictID, want.PositionID, want.PositionKey, want.CanonicalEvidenceID, want.CanonicalOwnerProfileID, want.OccurrenceID, want.OccurrenceOwnerProfileID, want.Quote, spanStart, want.SpanEnd, want.Authority, want.Submitted, at)
	}
	caseRows := func() *sqlmock.Rows {
		return sqlmock.NewRows([]string{"team_id", "conflict_id", "space_id", "space_generation", "status", "version", "preferred_position_id", "resolved_at", "resolution_reason", "created_at", "updated_at"})
	}
	caseRow := func() *sqlmock.Rows {
		return caseRows().AddRow(teamID, conflictID, "space", 7, "resolved", 3, want.PositionID, at, "reviewed", at.Add(-time.Hour), at)
	}
	queryError := errors.New("query failed")
	for _, scenario := range []string{"positions", "empty_positions", "case", "empty_case_positions", "missing", "case_query", "case_scan", "positions_query", "positions_scan", "positions_iteration"} {
		t.Run(scenario, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() {
				mock.ExpectClose()
				require.NoError(t, sqlDB.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
			db, err := gorm.Open(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}), &gorm.Config{})
			require.NoError(t, err)
			loadCase := scenario != "positions" && scenario != "empty_positions"
			if loadCase {
				q := mock.ExpectQuery(`FROM evidence_conflict_cases WHERE team_id = \$1::uuid AND conflict_id = \$2::uuid`).WithArgs(teamID, conflictID)
				switch scenario {
				case "missing":
					q.WillReturnRows(caseRows())
				case "case_query":
					q.WillReturnError(queryError)
				case "case_scan":
					q.WillReturnRows(caseRows().AddRow(teamID, conflictID, "space", "invalid", "open", 1, "", nil, "", at, at))
				default:
					q.WillReturnRows(caseRow())
				}
			}
			if scenario != "missing" && scenario != "case_query" && scenario != "case_scan" {
				q := mock.ExpectQuery(`FROM evidence_conflict_positions WHERE team_id = \$1::uuid AND conflict_id = \$2::uuid ORDER BY position_key, position_id`).WithArgs(teamID, conflictID)
				rows := positionRows()
				switch scenario {
				case "positions_query":
					q.WillReturnError(queryError)
				case "positions_scan":
					q.WillReturnRows(addPosition(rows, "invalid")).RowsWillBeClosed()
				case "positions_iteration":
					q.WillReturnRows(addPosition(rows, want.SpanStart).RowError(0, queryError)).RowsWillBeClosed()
				case "empty_positions", "empty_case_positions":
					q.WillReturnRows(rows).RowsWillBeClosed()
				default:
					q.WillReturnRows(addPosition(rows, want.SpanStart)).RowsWillBeClosed()
				}
			}
			if !loadCase {
				got, err := LoadEvidenceConflictPositions(t.Context(), db, teamID, conflictID)
				require.NoError(t, err)
				if scenario == "empty_positions" {
					require.Equal(t, []conflictcontract.EvidenceConflictPositionRecord{}, got)
				} else {
					require.Equal(t, []conflictcontract.EvidenceConflictPositionRecord{want}, got)
				}
				return
			}
			got, err := LoadCurrentEvidenceConflictCase(t.Context(), db, teamID, conflictID)
			switch scenario {
			case "missing":
				require.ErrorIs(t, err, conflictcontract.ErrEvidenceConflictNotFound)
				require.Nil(t, got)
			case "case_query", "positions_query", "positions_iteration":
				require.ErrorIs(t, err, queryError)
				require.Nil(t, got)
			case "case_scan", "positions_scan":
				require.Error(t, err)
				require.Nil(t, got)
			default:
				require.NoError(t, err)
				positions := []conflictcontract.EvidenceConflictPositionRecord{want}
				if scenario == "empty_case_positions" {
					positions = []conflictcontract.EvidenceConflictPositionRecord{}
				}
				require.Equal(t, &conflictcontract.EvidenceConflictCaseRecord{
					TeamID: teamID, ConflictID: conflictID, SpaceID: "space", SpaceGeneration: 7,
					Kind: "evidence_conflict", Status: "resolved", Version: 3,
					PreferredPositionID: want.PositionID, ResolvedAt: &at, ResolutionReason: "reviewed",
					CreatedAt: at.Add(-time.Hour), UpdatedAt: at, Positions: positions,
				}, got)
			}
		})
	}
}
