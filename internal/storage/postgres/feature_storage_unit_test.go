package postgres

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFeatureStorageUnitCursorUsesNativeOrderedKeys(t *testing.T) {
	source := featureSource{Name: "ontology_record_revisions", Key: []featureColumn{{Name: "team_id", Type: "uuid"}, {Name: "version", Type: "bigint"}}}
	query, args, err := featureBatchQuery(source, json.RawMessage(`["00000000-0000-0000-0000-000000000001",9007199254740993]`))
	require.NoError(t, err)
	require.Contains(t, query, `("team_id","version")>($1::uuid,$2::bigint)`)
	require.Contains(t, query, "LIMIT 100")
	require.Contains(t, query, "cumulative_bytes<=8388608")
	require.NotContains(t, query, "9007199254740993")
	require.Equal(t, json.Number("9007199254740993"), args[1])
	_, _, err = featureBatchQuery(source, json.RawMessage(`[1]`))
	require.ErrorContains(t, err, "key width changed")
	_, _, err = featureBatchQuery(featureSource{Name: "invalid"}, nil)
	require.ErrorContains(t, err, "no primary key")
}

func TestFeatureStorageUnitMigrationPoolRestoresAfterOverlappingStarters(t *testing.T) {
	db, err := sql.Open("pgx", "")
	require.NoError(t, err)
	defer db.Close()
	db.SetMaxOpenConns(1)
	first := reserveMigrationPool(db)
	second := reserveMigrationPool(db)
	require.Equal(t, 3, db.Stats().MaxOpenConnections)
	first()
	require.Equal(t, 2, db.Stats().MaxOpenConnections)
	second()
	require.Equal(t, 1, db.Stats().MaxOpenConnections)
	db.SetMaxOpenConns(4)
	release := reserveMigrationPool(db)
	release()
	require.Equal(t, 4, db.Stats().MaxOpenConnections)
}

func TestFeatureStorageUnitFingerprintBindsOrderAndExactContent(t *testing.T) {
	first := advanceFeatureFingerprint("", []byte(`{"text":"café 🧭","version":1}`))
	ordered := advanceFeatureFingerprint(first, []byte(`{"version":2}`))
	reversed := advanceFeatureFingerprint(advanceFeatureFingerprint("", []byte(`{"version":2}`)), []byte(`{"text":"café 🧭","version":1}`))
	require.NotEqual(t, ordered, reversed)
	require.NotEqual(t, first, advanceFeatureFingerprint("", []byte(`{"text":"cafe 🧭","version":1}`)))
	require.Len(t, ordered, 64)
}

func TestFeatureStorageUnitRegistrationRejectsIncompleteMigration(t *testing.T) {
	for _, source := range []string{"", "-- +goose Up\nSELECT 1"} {
		_, err := featureMigrationRegistration(FeatureStorageDataVersion, []byte(source), false)
		require.Error(t, err)
	}
	migration, err := featureMigrationRegistration(FeatureStorageDataVersion, []byte("-- +goose Up\nSELECT 1;\n-- +goose Down\nSELECT 2;"), false)
	require.NoError(t, err)
	require.NotNil(t, migration)
	reason := featureStorageFailureReason(&MigrationFailure{Reason: "ignored", Cause: nil})
	require.False(t, strings.Contains(reason, "ignored"))
}
