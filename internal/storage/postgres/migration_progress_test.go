package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
)

func TestEvidenceOnlyMigrationInventoryIdentity(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(MigrationsDir(), "v2_8", evidenceOnlyMigrationSQLName))
	require.NoError(t, err)
	groups := regexp.MustCompile(`INSERT INTO evidence_only_inventory SELECT unnest\(ARRAY\[(.*?)\]::text\[\]\),'(remove|retain|mixed|reset)';`).FindAllStringSubmatch(string(body), -1)
	require.Len(t, groups, 4)
	names := regexp.MustCompile(`'([^']+)'`)
	var entries []string
	seen := map[string]bool{}
	for _, group := range groups {
		for _, name := range names.FindAllStringSubmatch(group[1], -1) {
			require.False(t, seen[name[1]], "duplicate inventory table %s", name[1])
			seen[name[1]] = true
			entries = append(entries, name[1]+"\t"+group[2]+"\n")
		}
	}
	sort.Strings(entries)
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(entries, ""))))
	require.Equal(t, EvidenceOnlyMigrationInventorySHA256, digest)
	require.Contains(t, string(body), "-- Inventory identity: sha256:"+digest)
	require.Contains(t, string(body), "'inventory_sha256','"+digest+"'")
}

func progressNotice(t *testing.T, p *MigrationProgress, o MigrationObservation) string {
	t.Helper()
	body, err := json.Marshal(o)
	require.NoError(t, err)
	return migrationNoticePrefix + p.Snapshot().ExecutionID + ":" + string(body)
}
func TestMigrationProgressNoticeRoutingAndCapture(t *testing.T) {
	ctx, p := ObserveMigration(context.Background(), nil)
	defer p.Close()
	require.Same(t, p, MigrationProgressFromContext(ctx))
	_, other := ObserveMigration(context.Background(), nil)
	defer other.Close()
	p.snapshot.BackendPID = 123
	o := MigrationObservation{Sequence: 1, Phase: "sql_stream", WorkUnit: "migration", State: "started", Metric: "completed", Unit: "batches"}
	receiveMigrationNotice(999, progressNotice(t, p, o))
	require.Zero(t, p.Snapshot().SQLNotices)
	receiveMigrationNotice(123, progressNotice(t, p, o))
	require.EqualValues(t, 1, p.Snapshot().SQLNotices)
	require.Zero(t, other.Snapshot().SQLNotices)
	o.Sequence = 2
	o.State = "completed"
	receiveMigrationNotice(123, progressNotice(t, p, o))
	p.CaptureFinished()
	require.Equal(t, "complete", p.Snapshot().CaptureState)
	p.Close()
	o.Sequence = 3
	receiveMigrationNotice(123, progressNotice(t, p, o))
	require.EqualValues(t, 2, p.Snapshot().SQLNotices)
}
func TestMigrationProgressRejectsIncompleteOrUnsafeNotices(t *testing.T) {
	for _, mode := range []string{"missing", "gap", "negative", "unknown_field", "unknown_unit", "oversize", "trailing", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			_, p := ObserveMigration(context.Background(), nil)
			defer p.Close()
			p.snapshot.BackendPID = 123
			o := MigrationObservation{Sequence: 1, Phase: "sql_stream", WorkUnit: "migration", State: "started", Metric: "completed", Unit: "batches"}
			message := progressNotice(t, p, o)
			switch mode {
			case "missing":
				p.CaptureFinished()
				require.Equal(t, "incomplete", p.Snapshot().CaptureState)
				return
			case "gap":
				o.Sequence = 2
				message = progressNotice(t, p, o)
			case "negative":
				n := int64(-1)
				o.Completed = &n
				message = progressNotice(t, p, o)
			case "unknown_field":
				message = strings.TrimSuffix(message, "}") + `,"body":"private-canary"}`
			case "unknown_unit":
				o.WorkUnit = "private_canary"
				message = progressNotice(t, p, o)
			case "oversize":
				message += strings.Repeat("private-canary", 400)
			case "trailing":
				message += " {}"
			case "duplicate":
				receiveMigrationNotice(123, message)
			}
			receiveMigrationNotice(123, message)
			p.CaptureFinished()
			require.Equal(t, "incomplete", p.Snapshot().CaptureState)
		})
	}
}
func TestMigrationProgressCountsAndLiveness(t *testing.T) {
	var observations []MigrationSnapshot
	_, p := ObserveMigration(context.Background(), func(s MigrationSnapshot) { observations = append(observations, s) })
	defer p.Close()
	n, total := int64(1), int64(10)
	p.Report("ontology_reset", "ontology_catalog_heads", "progress", "affected", "rows", &n, &total)
	first := p.Snapshot()
	n = 2
	require.EqualValues(t, 1, *first.Completed)
	p.Report("ontology_reset", "ontology_catalog_heads", "progress", "affected", "rows", &n, &total)
	require.Len(t, observations, 1)
	require.Equal(t, first.LastProgressAt, p.Snapshot().LastProgressAt)
	p.lastLoop = time.Now().Add(-2 * time.Second)
	p.Report("ontology_reset", "ontology_catalog_heads", "progress", "affected", "rows", &n, &total)
	require.Len(t, observations, 2)
	require.True(t, p.Snapshot().LastProgressAt.After(first.LastProgressAt))
}
func TestMigrationProgressFailureDoesNotExposeDatabasePayload(t *testing.T) {
	original := &pgconn.PgError{Code: "22P02", Message: "private-canary legal hold retained fingerprint mismatch", Detail: "credential-canary", InternalQuery: "private-canary"}
	failure := &MigrationFailure{Cause: original, Reason: MigrationFailureReason(original)}
	require.NotContains(t, failure.Error(), "canary")
	require.Contains(t, failure.Error(), "SQLSTATE 22P02")
	var pgErr *pgconn.PgError
	require.ErrorAs(t, failure, &pgErr)
	require.Same(t, original, pgErr)
	require.True(t, errors.Is(&MigrationFailure{Cause: context.Canceled, Reason: "canceled"}, context.Canceled))
}

func TestMigrationProgressLegacyFailureReasons(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{"identity guard", &pgconn.PgError{Code: "P0001", Message: "identity cleanup blocked: usage history missing ownership aliases (7 rows)", Detail: "private-canary"}, "identity cleanup blocked: usage history missing ownership aliases"},
		{"identity dependencies", &pgconn.PgError{Code: "P0001", Message: "identity cleanup blocked: unexpected team_profiles dependencies: private-canary"}, "identity cleanup blocked: unexpected team_profiles dependencies"},
		{"spoofed identity prefix", &pgconn.PgError{Code: "P0001", Message: "identity cleanup blocked: usage history missing ownership aliases-private-canary"}, "database operation failed (SQLSTATE P0001)"},
		{"unrecognized guard", &pgconn.PgError{Code: "P0001", Message: "private-canary"}, "database operation failed (SQLSTATE P0001)"},
		{"native lock timeout", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout", Where: "private-canary"}, "lock timeout"},
		{"other lock failure", &pgconn.PgError{Code: "55P03", Message: "private-canary lock timeout"}, "database operation failed (SQLSTATE 55P03)"},
		{"wrong lock code", &pgconn.PgError{Code: "P0001", Message: "canceling statement due to lock timeout"}, "database operation failed (SQLSTATE P0001)"},
		{"wrapped ontology guard", fmt.Errorf("prepare ontology upgrade: %w: 7 unreconciled attempts require terminal evidence", ontology.ErrMaintenanceUpgradeBlocked), "ontology upgrade blocked: unreconciled attempts require terminal evidence"},
		{"untyped ontology message", errors.New("7 unreconciled attempts require terminal evidence"), "migration operation failed; database payload omitted"},
		{"other ontology guard", fmt.Errorf("%w: private-canary", ontology.ErrMaintenanceUpgradeBlocked), "ontology upgrade blocked"},
		{"canceled ontology", errors.Join(ontology.ErrMaintenanceUpgradeBlocked, context.Canceled), "canceled"},
		{"expired ontology", errors.Join(ontology.ErrMaintenanceUpgradeBlocked, context.DeadlineExceeded), "deadline_exceeded"},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := fmt.Errorf("startup: %w", test.err)
			failure := &MigrationFailure{Cause: original, Reason: MigrationFailureReason(original)}
			require.Equal(t, test.want, failure.Reason)
			require.NotContains(t, failure.Error(), "canary")
			require.ErrorIs(t, failure, test.err)
			var pgErr *pgconn.PgError
			if errors.As(test.err, &pgErr) {
				var preserved *pgconn.PgError
				require.ErrorAs(t, failure, &preserved)
				require.Same(t, pgErr, preserved)
			}
		})
	}
}
