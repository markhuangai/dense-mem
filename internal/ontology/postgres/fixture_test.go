package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/pressly/goose/v3"
	goosedb "github.com/pressly/goose/v3/database"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type ontologyFixture struct {
	admin       *gorm.DB
	app         *gorm.DB
	rls         *storage.RLS
	store       *Store
	knowledge   *knowledge.Store
	team        string
	credentials []*domain.Credential
	owners      []string
	space       string
	generation  int64
	schema      map[string]string
}

func newOntologyFixture(t *testing.T) *ontologyFixture {
	return newOntologyFixtureWithMaintenance(t, true)
}

func newOntologyFixtureWithMaintenance(t *testing.T, maintenance bool) *ontologyFixture {
	t.Helper()
	if os.Getenv("DENSE_MEM_REPOSITORY_TESTCONTAINERS") != "1" {
		t.Skip("set DENSE_MEM_REPOSITORY_TESTCONTAINERS=1 for disposable ontology PostgreSQL cases")
	}
	alias := "ontology-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	options := []testcontainers.ContainerCustomizer{postgrescontainer.WithDatabase("ontology_test"), postgrescontainer.WithUsername("testuser"), postgrescontainer.WithPassword("testpass"),
		testcontainers.WithWaitStrategy(wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(time.Minute))}
	labels := map[string]string{}
	for _, pair := range [][2]string{{"contract", "CONTRACT"}, {"repository", "REPOSITORY"}, {"run-id", "RUN_ID"}, {"run-attempt", "RUN_ATTEMPT"}, {"image-digest", "IMAGE_DIGEST"}} {
		if value := os.Getenv("DENSE_MEM_CI_PRECHECK_" + pair[1]); value != "" {
			labels["io.dense-mem.ci."+pair[0]] = value
		}
	}
	if project := os.Getenv("DENSE_MEM_CI_PRECHECK_PROJECT"); project != "" {
		labels["com.docker.compose.project"] = project
		labels["io.dense-mem.ci.phase"] = "precheck"
		labels["io.dense-mem.ci.scenario"] = "precheck"
		labels["io.dense-mem.ci.created-at"] = time.Now().UTC().Format(time.RFC3339)
	}
	if len(labels) > 0 {
		options = append(options, testcontainers.WithLabels(labels))
	}
	if name := os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK"); name != "" {
		options = append(options, network.WithNetworkName([]string{alias}, name))
	}
	container, err := postgrescontainer.Run(context.Background(), "pgvector/pgvector:0.8.2-pg18-trixie", options...)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, container.Terminate(context.Background())) })
	dsn, err := container.ConnectionString(context.Background(), "sslmode=disable")
	require.NoError(t, err)
	if os.Getenv("DENSE_MEM_CI_PRECHECK_NETWORK") != "" {
		dsn = "postgres://testuser:testpass@" + alias + ":5432/ontology_test?sslmode=disable"
	}
	admin, err := gorm.Open(gormpostgres.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	migrator, err := storage.NewMigrator(admin)
	require.NoError(t, err)
	require.NoError(t, migrator.RunUp(context.Background()))
	var schema map[string]string
	if !maintenance {
		schema = ontologySchemaSnapshot(t, admin)
		require.NoError(t, rollbackOntologySchemaTo(t, admin, 20261004190000))
	}
	require.NoError(t, admin.Exec(`CREATE ROLE ontology_app LOGIN PASSWORD 'ontology_test' NOSUPERUSER NOBYPASSRLS;
		GRANT USAGE ON SCHEMA public TO ontology_app;
		GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA public TO ontology_app;
		GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA public TO ontology_app;
		GRANT EXECUTE ON FUNCTION dense_mem_active_space_generation(UUID,UUID) TO ontology_app;
		GRANT EXECUTE ON FUNCTION dense_mem_lock_memory_space(UUID,UUID) TO ontology_app`).Error)
	parsed, err := url.Parse(dsn)
	require.NoError(t, err)
	parsed.User = url.UserPassword("ontology_app", "ontology_test")
	app, err := gorm.Open(gormpostgres.Open(parsed.String()), &gorm.Config{})
	require.NoError(t, err)
	t.Cleanup(func() {
		db, err := app.DB()
		require.NoError(t, err)
		require.NoError(t, db.Close())
		db, err = admin.DB()
		require.NoError(t, err)
		require.NoError(t, db.Close())
	})
	rls := storage.NewRLS()
	fixture := &ontologyFixture{admin: admin, app: app, rls: rls, store: NewStore(app, rls), knowledge: knowledge.NewStore(app, rls, knowledgecontract.ConflictRuntimeConfig{}), schema: schema}
	team := &domain.Team{Name: "ontology-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(admin, rls).Create(context.Background(), team))
	fixture.team = team.ID.String()
	for index := 0; index < 2; index++ {
		credential := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: fmt.Sprintf("owner-%d", index), KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
		require.NoError(t, access.NewCredentialRepository(admin, rls, nil).CreateCredential(context.Background(), credential))
		fixture.owners = append(fixture.owners, credential.ID.String())
		fixture.credentials = append(fixture.credentials, credential)
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, fixture.team).Row().Scan(&fixture.space, &fixture.generation)
	}))
	return fixture
}

func ontologySchemaSnapshot(t *testing.T, db *gorm.DB) map[string]string {
	t.Helper()
	var objects []struct{ Key, Definition string }
	require.NoError(t, db.Raw(`
		SELECT format('function:%I.%I(%s)',namespace.nspname,routine.proname,pg_get_function_identity_arguments(routine.oid)) AS key,
			pg_get_functiondef(routine.oid) AS definition
		FROM pg_proc routine JOIN pg_namespace namespace ON namespace.oid=routine.pronamespace
		WHERE namespace.nspname='public' AND routine.prorettype='trigger'::regtype
		UNION ALL
		SELECT 'trigger:'||namespace.nspname||'.'||relation.relname||'.'||trigger.tgname,
			format('%s; ALTER TABLE %I.%I %s TRIGGER %I',
				replace(pg_get_triggerdef(trigger.oid),'CREATE TRIGGER ','CREATE OR REPLACE TRIGGER '),
				namespace.nspname,relation.relname,
				CASE trigger.tgenabled WHEN 'O' THEN 'ENABLE' WHEN 'D' THEN 'DISABLE' WHEN 'R' THEN 'ENABLE REPLICA' WHEN 'A' THEN 'ENABLE ALWAYS' END,trigger.tgname)
		FROM pg_trigger trigger JOIN pg_class relation ON relation.oid=trigger.tgrelid
		JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname='public' AND NOT trigger.tgisinternal
		UNION ALL
		SELECT 'policy:'||namespace.nspname||'.'||relation.relname||'.'||policy.polname,
			format('DROP POLICY IF EXISTS %I ON %I.%I; CREATE POLICY %I ON %I.%I AS %s FOR %s TO %s%s%s',
				policy.polname,namespace.nspname,relation.relname,policy.polname,namespace.nspname,relation.relname,
				CASE WHEN policy.polpermissive THEN 'PERMISSIVE' ELSE 'RESTRICTIVE' END,
				CASE policy.polcmd WHEN '*' THEN 'ALL' WHEN 'r' THEN 'SELECT' WHEN 'a' THEN 'INSERT' WHEN 'w' THEN 'UPDATE' WHEN 'd' THEN 'DELETE' END,
				(SELECT string_agg(CASE WHEN role_id=0 THEN 'PUBLIC' ELSE quote_ident(pg_get_userbyid(role_id)) END,',' ORDER BY role_id) FROM unnest(policy.polroles) role_id),
				CASE WHEN policy.polqual IS NULL THEN '' ELSE ' USING ('||pg_get_expr(policy.polqual,policy.polrelid)||')' END,
				CASE WHEN policy.polwithcheck IS NULL THEN '' ELSE ' WITH CHECK ('||pg_get_expr(policy.polwithcheck,policy.polrelid)||')' END)
		FROM pg_policy policy JOIN pg_class relation ON relation.oid=policy.polrelid
		JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname='public'
		UNION ALL
		SELECT 'rls:'||namespace.nspname||'.'||relation.relname,
			format('ALTER TABLE %I.%I %s ROW LEVEL SECURITY; ALTER TABLE %I.%I %s ROW LEVEL SECURITY',
				namespace.nspname,relation.relname,CASE WHEN relation.relrowsecurity THEN 'ENABLE' ELSE 'DISABLE' END,
				namespace.nspname,relation.relname,CASE WHEN relation.relforcerowsecurity THEN 'FORCE' ELSE 'NO FORCE' END)
		FROM pg_class relation JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname='public' AND relation.relkind IN ('r','p')`).Scan(&objects).Error)
	snapshot := make(map[string]string, len(objects))
	for _, object := range objects {
		snapshot[object.Key] = object.Definition
	}
	return snapshot
}

func (f *ontologyFixture) restoreOntologySchema(t *testing.T) {
	t.Helper()
	require.NotEmpty(t, f.schema)
	_, err := ontologyMigrationProvider(t, f.admin).Up(context.Background())
	require.NoError(t, err)
	current := ontologySchemaSnapshot(t, f.admin)
	keys := make([]string, 0, len(f.schema))
	for key := range f.schema {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	// Ontology rollback also removes later private-memory hooks; restore their exact migrated definitions.
	for _, key := range keys {
		if current[key] != f.schema[key] {
			require.NoError(t, f.admin.Exec(f.schema[key]).Error, key)
		}
	}
	current = ontologySchemaSnapshot(t, f.admin)
	require.Len(t, current, len(f.schema))
	for _, key := range keys {
		require.Equal(t, f.schema[key], current[key], key)
	}
}

// Exercise ontology rollback guards without crossing unrelated irreversible migration boundaries.
func ontologyMigrationProvider(t *testing.T, db *gorm.DB) *goose.Provider {
	t.Helper()
	paths, err := filepath.Glob("../../../migrations/postgres/v2_6/*_ontology_*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, paths)
	files := fstest.MapFS{}
	for _, path := range paths {
		contents, err := os.ReadFile(path)
		require.NoError(t, err)
		files[filepath.Base(path)] = &fstest.MapFile{Data: contents}
	}
	sqlDB, err := db.DB()
	require.NoError(t, err)
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, files,
		goose.WithDisableGlobalRegistry(true), goose.WithAllowOutofOrder(true))
	require.NoError(t, err)
	return provider
}

func rollbackOntologySchemaTo(t *testing.T, db *gorm.DB, target int64) error {
	t.Helper()
	ctx := context.Background()
	provider := ontologyMigrationProvider(t, db)
	versions, err := goosedb.NewStore(goosedb.DialectPostgres, "goose_db_version")
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	applied, err := versions.ListMigrations(ctx, sqlDB)
	if err != nil {
		return err
	}
	sources := provider.ListSources()
	slices.Reverse(sources)
	for _, source := range sources {
		if source.Version <= target {
			break
		}
		if !slices.ContainsFunc(applied, func(version *goosedb.ListMigrationsResult) bool { return version.Version == source.Version }) {
			continue
		}
		if _, err := provider.ApplyVersion(ctx, source.Version, false); err != nil {
			return err
		}
	}
	return nil
}

func (f *ontologyFixture) actor(owner int, role string) context.Context {
	credential := f.credentials[owner]
	return requestctx.WithActor(context.Background(), requestctx.Actor{
		TeamID: credential.TeamID, OwnerID: credential.OwnerID, IdentityID: credential.ActorIdentityID,
		MembershipID: credential.MembershipID, CredentialID: &credential.ID, AuthMethod: "api_key",
		Role: role, Grants: credential.Scopes, AllowedSpaces: credential.MemorySpaces(),
	})
}

func (f *ontologyFixture) evidence(t *testing.T, owner int, text string) knowledgecontract.EvidenceIngestResult {
	t.Helper()
	result, err := f.knowledge.CreateIngestForTest(context.Background(), knowledgecontract.CreateIngestInput{TeamID: f.team, OwnerProfileID: f.owners[owner], IdempotencyKey: uuid.NewString(), RequestHash: testHash(text), Evidence: []knowledgecontract.EvidenceInput{{Content: text}}})
	require.NoError(t, err)
	return *result
}

func (f *ontologyFixture) evidenceAt(t *testing.T, owner int, text, created string) ontology.SourceHandle {
	t.Helper()
	seed := f.evidence(t, owner, text)
	ingestID, fragmentID := uuid.NewString(), uuid.NewString()
	require.NoError(t, f.rls.WithSystemTx(context.Background(), f.admin, func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO knowledge_ingests
			(ingest_id,team_id,owner_profile_id,space_id,space_generation,request_hash,source_summary,status,proposal,metadata,created_at,updated_at,completed_at)
			SELECT ?::uuid,team_id,owner_profile_id,space_id,space_generation,request_hash,source_summary,status,proposal,metadata,?::timestamptz,?::timestamptz,?::timestamptz
			FROM knowledge_ingests WHERE team_id=?::uuid AND ingest_id=?::uuid`, ingestID, created, created, created, f.team, seed.IngestID).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO evidence_fragments
			(fragment_id,team_id,ingest_id,owner_profile_id,space_id,space_generation,evidence_index,content,content_hash,source_type,authority,source_ref,labels,metadata,force_insert,created_at)
			SELECT ?::uuid,team_id,?::uuid,owner_profile_id,space_id,space_generation,0,content,content_hash,source_type,authority,source_ref,labels,metadata,true,?::timestamptz
			FROM evidence_fragments WHERE team_id=?::uuid AND fragment_id=?::uuid`, fragmentID, ingestID, created, f.team, seed.Evidence[0].FragmentID).Error
	}))
	return ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: fragmentID, Version: 1}
}

func (f *ontologyFixture) source(t *testing.T, handle ontology.SourceHandle) ontology.SourceDependency {
	t.Helper()
	sources, err := f.store.ReadSources(context.Background(), f.team, []ontology.SourceHandle{handle})
	require.NoError(t, err)
	require.Len(t, sources, 1)
	fingerprint, err := ontology.SourceFingerprint(sources[0])
	require.NoError(t, err)
	return ontology.SourceDependency{SourceHandle: handle, Fingerprint: fingerprint}
}

func testHash(text string) string {
	digest := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(digest[:])
}
func testTopic(key string) ontology.Record {
	return ontology.Record{ID: uuid.NewString(), Kind: ontology.Topic, Definition: &ontology.Definition{Key: key, Label: key}}
}
func testPublication(key string, revision int64, changes ...ontology.Change) ontology.Publication {
	return ontology.Publication{OperationKey: key, ExpectedRevision: revision, Reason: "synthetic ontology fixture", Changes: changes}
}

func (f *ontologyFixture) canonicalSnapshot(t *testing.T) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, table := range []string{"evidence_fragments", "evidence_occurrences", "evidence_sources", "evidence_source_revisions", "entity_records", "entity_names", "value_records", "relationship_records", "relationship_evidence_supports", "relationship_support_decision_events", "evidence_lifecycle_events", "evidence_quarantines"} {
		var value string
		require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM `+table+` AS row WHERE team_id=?::uuid`, f.team).Row().Scan(&value))
		result[table] = value
	}
	return result
}
