package evalharness

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	ontology "github.com/markhuangai/dense-mem/internal/topics/contract"
)

// PreparedTopics identifies one derived organization over an immutable imported source seed.
type PreparedTopics struct {
	Version            int    `json:"version"`
	Arm                string `json:"arm"`
	ParentImage        string `json:"parent_seed_image"`
	Image              string `json:"application_image"`
	SourceRevision     string `json:"source_revision"`
	OrganizationHash   string `json:"organization_sha256"`
	Eligible           int    `json:"eligible_evidence"`
	Assigned           int    `json:"assigned_evidence"`
	Memberships        int    `json:"direct_memberships"`
	Topics             int    `json:"topics"`
	TerminalPlacements int    `json:"terminal_placements"`
}

func measurePreparedTopics(ctx context.Context, n *Native, manifest Manifest, snapshot NativeObservation) (PreparedTopics, error) {
	var sealed PreparedTopics
	if (manifest.PairArm == "baseline") != n.legacyOrganization {
		return sealed, errors.New("paired application arm does not match its database layout")
	}
	if err := preparedNativeReady(snapshot, manifest, n.Scope.TeamID); err != nil {
		return sealed, err
	}
	coverage, err := n.measureTopicCoverage(ctx, manifest, true)
	if err != nil {
		return sealed, err
	}
	if coverage.Eligible != int(snapshot.Ontology.Counts.Eligible) || coverage.Eligible < 1 || coverage.Assigned != coverage.Eligible || coverage.Topics < 1 || (manifest.PairArm == "candidate" && coverage.TerminalPlacements != coverage.Topics) {
		return sealed, fmt.Errorf("incomplete prepared topics: eligible=%d assigned=%d topics=%d placements=%d", coverage.Eligible, coverage.Assigned, coverage.Topics, coverage.TerminalPlacements)
	}
	return coverage, nil
}

func preparedNativeReady(observation NativeObservation, manifest Manifest, team string) error {
	status := observation.Ontology
	// A restored, paused seed can outlive the preparation window; its persisted policy remains observable.
	if status.Window == nil && status.Paused {
		status.Window = &ontology.MaintenanceWindow{Policy: status.PendingPolicy}
		observation.Ontology = status
	}
	return NativeReady(observation, true, true, manifest, team)
}

func (n *Native) measureTopicCoverage(ctx context.Context, manifest Manifest, fingerprint bool) (PreparedTopics, error) {
	result := PreparedTopics{Version: 1, Arm: manifest.PairArm, ParentImage: manifest.ParentSeedImage, Image: manifest.Image, SourceRevision: manifest.SourceRevision}
	tx, err := n.systemTx(ctx)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	sql := `WITH eligible AS (SELECT source_id::uuid AS id FROM topic_maintenance_sources WHERE team_id=$1::uuid AND shared_space_id=$2::uuid AND space_generation=$3 AND source_kind='evidence' AND eligible),
	heads AS MATERIALIZED (SELECT h.* FROM topic_record_heads h WHERE h.team_id=$1::uuid AND h.shared_space_id=$2::uuid AND h.space_generation=$3 AND NOT h.retired),
	topics AS (SELECT h.record_id,h.version FROM heads h WHERE h.kind='topic'),
	members AS (SELECT DISTINCT eligible.id AS evidence_id,h.topic_id FROM eligible
		JOIN topic_record_heads h ON h.team_id=$1::uuid AND h.shared_space_id=$2::uuid AND h.space_generation=$3 AND h.kind='assignment' AND NOT h.retired AND h.evidence_id=eligible.id
		JOIN topics t ON t.record_id=h.topic_id
		LEFT JOIN LATERAL (SELECT a.status FROM topic_assignment_outcomes a
			WHERE a.team_id=$1::uuid AND a.shared_space_id=$2::uuid AND a.space_generation=$3 AND a.evidence_id=eligible.id
			ORDER BY a.created_at DESC,a.outcome_id DESC LIMIT 1) verdict ON TRUE
		WHERE COALESCE(verdict.status,'assigned')='assigned'),
	decisions AS (SELECT t.record_id,d.status,d.parent_topic_id,d.parent_topic_version,d.outcome_id
		FROM topics t LEFT JOIN LATERAL (SELECT status,parent_topic_id,parent_topic_version,outcome_id FROM community_placement_outcomes d
			WHERE d.team_id=$1::uuid AND d.shared_space_id=$2::uuid AND d.space_generation=$3 AND d.topic_id=t.record_id AND d.topic_version=t.version
			ORDER BY d.created_at DESC,d.outcome_id DESC LIMIT 1) d ON TRUE
		WHERE d.status='standalone' OR (d.status='placed' AND EXISTS(SELECT 1 FROM topics p WHERE p.record_id=d.parent_topic_id AND p.version=d.parent_topic_version))),
	identity AS (SELECT 'head' AS kind,to_jsonb(h)::text AS body FROM heads h UNION ALL SELECT 'placement',to_jsonb(d)::text FROM decisions d)
	SELECT (SELECT count(*) FROM eligible),(SELECT count(DISTINCT evidence_id) FROM members),(SELECT count(*) FROM members),
	(SELECT count(*) FROM topics),(SELECT count(*) FROM decisions),
	CASE WHEN $4::boolean THEN (SELECT encode(sha256(convert_to(COALESCE(string_agg(encode(sha256(convert_to(body,'UTF8')),'hex'),'' ORDER BY kind,body),''),'UTF8')),'hex') FROM identity) ELSE '' END`
	if n.legacyOrganization {
		sql = `WITH eligible AS (SELECT source_id AS id FROM ontology_maintenance_sources WHERE team_id=$1::uuid AND shared_space_id=$2::uuid AND space_generation=$3 AND source_kind='evidence' AND eligible),
		heads AS MATERIALIZED (SELECT h.* FROM ontology_record_heads h WHERE h.team_id=$1::uuid AND h.shared_space_id=$2::uuid AND h.space_generation=$3 AND NOT h.retired),
		members AS (SELECT DISTINCT r.body#>>'{assignment,source,id}' AS evidence_id,t.record_id FROM heads h JOIN ontology_revision_records r USING(team_id,shared_space_id,space_generation,record_id,version)
			JOIN heads t ON t.record_id::text=r.body#>>'{assignment,definition_id}' AND t.kind='topic' JOIN eligible ON eligible.id=r.body#>>'{assignment,source,id}' WHERE h.kind='assignment' AND r.body#>>'{assignment,source,kind}'='evidence')
		SELECT (SELECT count(*) FROM eligible),(SELECT count(DISTINCT evidence_id) FROM members),(SELECT count(*) FROM members),
		(SELECT count(*) FROM heads WHERE kind='topic'),0,CASE WHEN $4::boolean THEN (SELECT encode(sha256(convert_to(COALESCE(string_agg(encode(sha256(convert_to(to_jsonb(heads)::text,'UTF8')),'hex'),'' ORDER BY record_id),''),'UTF8')),'hex') FROM heads) ELSE '' END`
	}
	err = tx.QueryRowContext(ctx, sql, n.Scope.TeamID, n.Scope.SpaceID, n.Scope.Generation, fingerprint).Scan(&result.Eligible, &result.Assigned, &result.Memberships, &result.Topics, &result.TerminalPlacements, &result.OrganizationHash)
	return result, err
}

func PrepareTopics(ctx context.Context, manifest Manifest, corpus []CorpusItem, client *HTTPClient, n *Native) (retErr error) {
	work, stop := context.WithDeadline(ctx, manifest.ModelDeadline)
	defer stop()
	seed, _, _, snapshot, err := verifySeedContent(work, manifest, corpus, client, n)
	if err != nil {
		return err
	}
	if seed.PreparedTopics != nil || (manifest.PairArm == "baseline") != n.legacyOrganization {
		return errors.New("preparation must start from the matching undeveloped parent seed")
	}
	if manifest.ResumeScope == nil || !snapshot.Ontology.Enabled {
		if err := NativeReady(snapshot, false, true, manifest, n.Scope.TeamID); err != nil {
			return err
		}
	}
	observerURL, err := url.Parse(os.Getenv("EVAL_DATABASE_URL"))
	if err != nil {
		return errors.New("invalid local preparation observer address")
	}
	observerURL.Path = "/postgres"
	observer, err := sql.Open("pgx", observerURL.String())
	if err != nil {
		return errors.New("open local PostgreSQL observation database")
	}
	defer observer.Close()
	before, err := readPreparationWork(work, observer)
	if err != nil {
		return err
	}
	previous, err := n.measureTopicCoverage(work, manifest, false)
	if err != nil {
		return err
	}
	origin, err := preparationOrigin(work, observer, manifest, before, previous.Assigned, snapshot.Ontology.Enabled)
	if err != nil {
		return err
	}
	if err := n.EnableOntology(work, manifest); err != nil {
		return err
	}
	defer func() {
		if retErr != nil {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
			defer cancel()
			retErr = errors.Join(retErr, n.Pause(cleanup))
		}
	}()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		var status ontology.MaintenanceStatus
		if err = n.Control(work, http.MethodGet, "/topic-maintenance/status", nil, &status); err != nil {
			return err
		}
		if status.ConfigurationError != "" || status.Counts.Failed > 0 || status.Counts.Ambiguous > 0 {
			return fmt.Errorf("organization is blocked: failed=%d ambiguous=%d", status.Counts.Failed, status.Counts.Ambiguous)
		}
		if status.CoverageComplete && status.DiscoveryComplete && status.Counts.Eligible > 0 && status.Counts.Pending == 0 && status.ActiveWork == 0 {
			coverage, err := n.measureTopicCoverage(work, manifest, false)
			if err != nil {
				return err
			}
			if coverage.Eligible != coverage.Assigned {
				return fmt.Errorf("assigned seed gate rejected %d unassigned eligible Evidence items", coverage.Eligible-coverage.Assigned)
			}
			if coverage.Topics > 0 && (n.legacyOrganization || coverage.Topics == coverage.TerminalPlacements) {
				break
			}
		}
		if err = client.Capture.Emit("seed_organization_progress", status.Counts); err != nil {
			return err
		}
		select {
		case <-work.Done():
			return work.Err()
		case <-ticker.C:
		}
	}
	if err = n.Pause(work); err != nil {
		return err
	}
	final, err := waitNative(work, n, manifest, true)
	if err != nil {
		return err
	}
	if final.CanonicalHash != seed.CanonicalHash {
		return errors.New("topic preparation changed canonical data")
	}
	sealed, err := measurePreparedTopics(work, n, manifest, final)
	if err != nil {
		return err
	}
	after, err := readPreparationWork(work, observer)
	if err != nil {
		return err
	}
	if after.Reset != origin.Counters.Reset || after.Dealloc != origin.Counters.Dealloc || after.Calls < origin.Counters.Calls || after.WrittenRows < origin.Counters.WrittenRows {
		return errors.New("database statistics reset or evicted statements during preparation")
	}
	attempts, err := MeasureAttempts(final.ProviderAttempts)
	if err != nil {
		return err
	}
	seed.PreparationWork = &PreparationWork{SQLStatements: after.Calls - origin.Counters.Calls, WrittenRows: after.WrittenRows - origin.Counters.WrittenRows, NewAssignments: sealed.Assigned - origin.Assigned, NewMemberships: sealed.Memberships, ElapsedMS: time.Since(origin.Started).Milliseconds(), ProviderAttempts: attempts.Attempts, UnknownUsage: attempts.UnknownUsage, PartialUsage: attempts.PartialUsage, TotalTokens: attempts.Total, InputTokens: attempts.Input, OutputTokens: attempts.Output}
	seed.PreparedTopics, seed.Version = &sealed, 1
	if err = n.DB.QueryRowContext(work, `SELECT COALESCE(max(version_id),0) FROM (SELECT DISTINCT ON (version_id) version_id,is_applied FROM goose_db_version ORDER BY version_id,id DESC) latest WHERE is_applied`).Scan(&seed.SchemaVersion); err != nil {
		return err
	}
	return writeJSON(filepath.Join(manifest.SeedDir, "seed.json"), seed)
}

// PostgreSQL statements and row writes include all work in the isolated application database during the measured preparation invocation.
type PreparationWork struct {
	SQLStatements    int64 `json:"sql_statements"`
	WrittenRows      int64 `json:"written_rows"`
	NewAssignments   int   `json:"net_new_assigned"`
	NewMemberships   int   `json:"net_new_direct_memberships"`
	ElapsedMS        int64 `json:"wall_elapsed_ms_including_resumes"`
	ProviderAttempts int   `json:"cumulative_provider_attempts"`
	UnknownUsage     int   `json:"cumulative_unknown_usage"`
	PartialUsage     int   `json:"cumulative_partial_usage"`
	TotalTokens      int64 `json:"cumulative_reported_total_tokens"`
	InputTokens      int64 `json:"cumulative_reported_input_tokens"`
	OutputTokens     int64 `json:"cumulative_reported_output_tokens"`
}
type prepCounters struct {
	Calls, WrittenRows, Dealloc int64
	Reset                       string
}

func readPreparationWork(ctx context.Context, db *sql.DB) (prepCounters, error) {
	var totals prepCounters
	err := db.QueryRowContext(ctx, `SELECT COALESCE((SELECT sum(calls) FROM pg_stat_statements WHERE dbid=d.datid AND toplevel),0),
		d.tup_inserted+d.tup_updated+d.tup_deleted,i.dealloc,concat_ws('/',i.stats_reset::text,d.stats_reset::text)
		FROM pg_stat_database d CROSS JOIN pg_stat_statements_info i WHERE d.datname='densemem'`).Scan(&totals.Calls, &totals.WrittenRows, &totals.Dealloc, &totals.Reset)
	return totals, err
}

type preparationStart struct {
	Counters prepCounters
	Assigned int
	Started  time.Time
}

// The observer database survives a resumed local volume and is excluded from the published application dump.
func preparationOrigin(ctx context.Context, observer *sql.DB, m Manifest, now prepCounters, assigned int, enabled bool) (preparationStart, error) {
	var origin preparationStart
	if _, err := observer.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS eval_topic_preparation_start (
		singleton boolean PRIMARY KEY CHECK(singleton), identity text NOT NULL, started_at timestamptz NOT NULL,
		sql_calls bigint NOT NULL, written_rows bigint NOT NULL, dealloc bigint NOT NULL, reset_stamp text NOT NULL, assigned integer NOT NULL)`); err != nil {
		return origin, err
	}
	key := checkpointFingerprint(m)
	if !enabled && assigned == 0 {
		if _, err := observer.ExecContext(ctx, `INSERT INTO eval_topic_preparation_start VALUES(true,$1,clock_timestamp(),$2,$3,$4,$5,$6)
			ON CONFLICT(singleton) DO NOTHING`, key, now.Calls, now.WrittenRows, now.Dealloc, now.Reset, assigned); err != nil {
			return origin, err
		}
	}
	err := observer.QueryRowContext(ctx, `SELECT started_at,sql_calls,written_rows,dealloc,reset_stamp,assigned FROM eval_topic_preparation_start WHERE singleton AND identity=$1`, key).
		Scan(&origin.Started, &origin.Counters.Calls, &origin.Counters.WrittenRows, &origin.Counters.Dealloc, &origin.Counters.Reset, &origin.Assigned)
	if errors.Is(err, sql.ErrNoRows) {
		return origin, errors.New("cannot verify first preparation counters for this restored seed")
	}
	return origin, err
}
