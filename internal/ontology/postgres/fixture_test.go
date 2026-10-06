package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/postgres"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/network"
	"github.com/testcontainers/testcontainers-go/wait"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

type ontologyFixture struct {
	admin      *gorm.DB
	app        *gorm.DB
	rls        *storage.RLS
	store      *Store
	knowledge  *knowledge.Store
	team       string
	owners     []string
	space      string
	generation int64
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
	if !maintenance {
		require.NoError(t, migrator.RunDown(context.Background()))
		require.NoError(t, migrator.RunDown(context.Background()))
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
	fixture := &ontologyFixture{admin: admin, app: app, rls: rls, store: NewStore(app, rls), knowledge: knowledge.NewStore(app, rls, knowledgecontract.ConflictRuntimeConfig{})}
	team := &domain.Team{Name: "ontology-" + uuid.NewString()}
	require.NoError(t, access.NewTeamRepository(admin, rls).Create(context.Background(), team))
	fixture.team = team.ID.String()
	for index := 0; index < 2; index++ {
		credential := &domain.Credential{ID: uuid.New(), TeamID: team.ID, Name: fmt.Sprintf("owner-%d", index), KeyHash: "synthetic-" + uuid.NewString(), KeyPrefix: strings.ReplaceAll(uuid.NewString(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
		require.NoError(t, access.NewCredentialRepository(admin, rls, nil).CreateCredential(context.Background(), credential))
		fixture.owners = append(fixture.owners, credential.ID.String())
	}
	require.NoError(t, rls.WithSystemTx(context.Background(), admin, func(tx *gorm.DB) error {
		return tx.Raw(`SELECT id::text,generation FROM memory_spaces WHERE team_id=?::uuid AND kind='team_shared'`, fixture.team).Row().Scan(&fixture.space, &fixture.generation)
	}))
	return fixture
}

func (f *ontologyFixture) actor(owner int, role string) context.Context {
	return requestctx.WithActor(context.Background(), requestctx.Actor{TeamID: uuid.MustParse(f.team), OwnerID: uuid.MustParse(f.owners[owner]), Role: role, Grants: []string{"read", "write"},
		AllowedSpaces: []domain.MemorySpaceAccess{{ID: uuid.MustParse(f.space), Kind: domain.MemorySpaceTeamShared, Generation: f.generation}}})
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
