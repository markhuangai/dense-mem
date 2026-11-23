//go:build integration && ontology_study

package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

const studyRevisionJoin = "JOIN ontology_revision_records AS revision USING(team_id,shared_space_id,space_generation,record_id,version)"

var studyBindPattern = regexp.MustCompile(`\$[0-9]+`)

func studyBoundLookupSHA(sql string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(studyBindPattern.ReplaceAllString(sql, "?"))))
}

func studyVectorPattern() *regexp.Regexp {
	before, after, _ := strings.Cut(vocabularyVectorSQL, "?")
	return regexp.MustCompile(regexp.QuoteMeta(before) + `(\$[0-9]+)` + regexp.QuoteMeta(after))
}

func studyVectorSQL(query string, pattern *regexp.Regexp) (string, error) {
	if strings.Count(query, studyRevisionJoin) != 1 || len(pattern.FindAllStringIndex(query, 3)) != 2 {
		return "", fmt.Errorf("vocabulary SQL no longer matches scoped vector projection")
	}
	joined := studyRevisionJoin + " JOIN ontology_study_topic_vectors AS studyvec USING(team_id,shared_space_id,space_generation,record_id,version)"
	query = strings.Replace(query, studyRevisionJoin, joined, 1)
	return pattern.ReplaceAllString(query, `ontology_study_cached(studyvec.document,$1::text[])`), nil
}

type studyVectorInstall struct {
	Rows                                         int
	SetupMS, BackfillMS, IndexMS, VerificationMS float64
	TotalMS                                      float64
}

func hasStudyVectors(arm string) bool {
	return arm == "vector" || arm == "vector-skip" || arm == "rank-first"
}

func installStudyVectors(t *testing.T, f *ontologyFixture, arm string) studyVectorInstall {
	t.Helper()
	var result studyVectorInstall
	if !hasStudyVectors(arm) {
		return result
	}
	start := time.Now()
	script, err := os.ReadFile(os.Getenv("ONTOLOGY_STUDY_VECTOR_SQL"))
	require.NoError(t, err)
	parts := regexp.MustCompile(`(?m)^-- study phase: (?:backfill|index)$`).Split(string(script), -1)
	require.Len(t, parts, 3)
	require.NoError(t, f.admin.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL app.tx_mode='migration'").Error; err != nil {
			return err
		}
		stage := time.Now()
		if err := tx.Exec(parts[0]).Error; err != nil {
			return err
		}
		result.SetupMS = studyMS(time.Since(stage))
		stage = time.Now()
		backfill := tx.Exec(parts[1])
		if backfill.Error != nil {
			return backfill.Error
		}
		result.Rows = int(backfill.RowsAffected)
		result.BackfillMS = studyMS(time.Since(stage))
		stage = time.Now()
		err := tx.Exec(parts[2]).Error
		result.IndexMS = studyMS(time.Since(stage))
		return err
	}))
	verifyStart := time.Now()
	require.Equal(t, result.Rows, verifyStudyVectors(t, f))
	var visible int
	require.NoError(t, f.app.Raw("SELECT count(*) FROM ontology_study_topic_vectors").Row().Scan(&visible))
	require.Zero(t, visible, "non-bypass connection outside any team scope")
	var forced, valid, bypass bool
	require.NoError(t, f.app.Raw(`SELECT relforcerowsecurity,indisvalid,rolsuper OR rolbypassrls FROM pg_class
		JOIN pg_index ON indrelid=pg_class.oid JOIN pg_roles ON rolname=current_user
		WHERE pg_class.oid='ontology_study_topic_vectors'::regclass
		AND indexrelid='ontology_study_topic_vectors_gin'::regclass`).Row().Scan(&forced, &valid, &bypass))
	require.True(t, forced && valid && !bypass)
	result.VerificationMS = studyMS(time.Since(verifyStart))
	result.TotalMS = studyMS(time.Since(start))
	return result
}

func verifyStudyVectors(t *testing.T, f *ontologyFixture) int {
	t.Helper()
	var total int
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		var mismatches int
		err := tx.Raw(`SELECT count(*),count(*) FILTER (WHERE studyvec.document IS DISTINCT FROM `+vocabularyVectorSQL+`)
			FROM ontology_record_heads AS head JOIN ontology_revision_records AS revision
			USING(team_id,shared_space_id,space_generation,record_id,version)
			LEFT JOIN ontology_study_topic_vectors AS studyvec USING(team_id,shared_space_id,space_generation,record_id,version)
			WHERE head.team_id=?::uuid AND head.shared_space_id=?::uuid AND head.space_generation=?
			AND head.kind='topic' AND revision.kind='topic' AND NOT head.retired`,
			pq.Array(ontology.VocabularyStopWords()), fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&total, &mismatches)
		if err != nil {
			return err
		}
		if total == 0 || mismatches != 0 {
			return fmt.Errorf("current topic vectors total=%d mismatches=%d", total, mismatches)
		}
		return nil
	}))
	return total
}

type studyPlanNode struct {
	Type          string          `json:"Node Type"`
	Subplan       string          `json:"Subplan Name"`
	Relation      string          `json:"Relation Name"`
	Index         string          `json:"Index Name"`
	Rows          float64         `json:"Actual Rows"`
	Filtered      float64         `json:"Rows Removed by Filter"`
	IndexFiltered float64         `json:"Rows Removed by Index Recheck"`
	Loops         int             `json:"Actual Loops"`
	Plans         []studyPlanNode `json:"Plans"`
}

type studyQueryPlan struct {
	Indexes, Relations                        []string
	PlanningMS, ExecutionMS                   float64
	Raw                                       json.RawMessage
	RankedPageRows, PayloadRowsVisited        float64
	PageBeforePayload, PayloadKeyedWithinPage bool
}

func explainStudyQuery(t *testing.T, f *ontologyFixture, stats *studyRankQueries) studyQueryPlan {
	t.Helper()
	var sample []struct {
		Root      studyPlanNode `json:"Plan"`
		Planning  float64       `json:"Planning Time"`
		Execution float64       `json:"Execution Time"`
	}
	var body []byte
	require.NotEmpty(t, stats.exampleSQL)
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, _ scope) error {
		err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+stats.exampleSQL, stats.exampleArgs...).Scan(&body)
		if err != nil {
			return err
		}
		return json.Unmarshal(body, &sample)
	}))
	require.Len(t, sample, 1)
	plan := studyQueryPlan{PlanningMS: sample[0].Planning, ExecutionMS: sample[0].Execution, Raw: body}
	var pageFound, rankedPayload, unkeyedPayload bool
	var visit func(studyPlanNode, bool)
	visit = func(node studyPlanNode, insidePage bool) {
		if node.Loops == 0 {
			return
		}
		if node.Relation != "" {
			plan.Relations = append(plan.Relations, node.Relation)
		}
		if node.Index != "" {
			plan.Indexes = append(plan.Indexes, node.Index)
		}
		if node.Subplan == "CTE page" {
			insidePage = true
			pageFound = node.Type == "Limit"
			plan.RankedPageRows = node.Rows
		}
		if strings.HasSuffix(node.Relation, "_revision_payloads") {
			plan.PayloadRowsVisited += (node.Rows + node.Filtered + node.IndexFiltered) * float64(node.Loops)
			rankedPayload = rankedPayload || insidePage
			unkeyedPayload = unkeyedPayload || node.Index == ""
		}
		for _, child := range node.Plans {
			visit(child, insidePage)
		}
	}
	visit(sample[0].Root, false)
	plan.PageBeforePayload = pageFound && !rankedPayload
	plan.PayloadKeyedWithinPage = plan.PageBeforePayload && !unkeyedPayload && plan.RankedPageRows >= plan.PayloadRowsVisited
	slices.Sort(plan.Relations)
	plan.Relations = slices.Compact(plan.Relations)
	slices.Sort(plan.Indexes)
	plan.Indexes = slices.Compact(plan.Indexes)
	return plan
}
