//go:build integration && ontology_study

package postgres

import (
	"context"
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

func installStudyVectors(t *testing.T, f *ontologyFixture, arm string) float64 {
	t.Helper()
	if arm != "vector" {
		return 0
	}
	script, err := os.ReadFile(os.Getenv("ONTOLOGY_STUDY_VECTOR_SQL"))
	require.NoError(t, err)
	start := time.Now()
	require.NoError(t, f.admin.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL app.tx_mode='migration'").Error; err != nil {
			return err
		}
		return tx.Exec(string(script)).Error
	}))
	verifyStudyVectors(t, f)
	var visible int
	require.NoError(t, f.app.Raw("SELECT count(*) FROM ontology_study_topic_vectors").Row().Scan(&visible))
	require.Zero(t, visible, "non-bypass connection outside any team scope")
	var forced, valid, bypass bool
	require.NoError(t, f.app.Raw(`SELECT relforcerowsecurity,indisvalid,rolsuper OR rolbypassrls FROM pg_class
		JOIN pg_index ON indrelid=pg_class.oid JOIN pg_roles ON rolname=current_user
		WHERE pg_class.oid='ontology_study_topic_vectors'::regclass
		AND indexrelid='ontology_study_topic_vectors_gin'::regclass`).Row().Scan(&forced, &valid, &bypass))
	require.True(t, forced && valid && !bypass)
	return float64(time.Since(start).Microseconds()) / 1000
}

func verifyStudyVectors(t *testing.T, f *ontologyFixture) {
	t.Helper()
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, fence scope) error {
		var total, mismatches int
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
}

type studyPlanNode struct {
	Relation string          `json:"Relation Name"`
	Index    string          `json:"Index Name"`
	Loops    int             `json:"Actual Loops"`
	Plans    []studyPlanNode `json:"Plans"`
}

type studyQueryPlan struct {
	Indexes                 []string
	Relations               []string
	PlanningMS, ExecutionMS float64
}

func explainStudyQuery(t *testing.T, f *ontologyFixture, stats *studyRankQueries) studyQueryPlan {
	t.Helper()
	var sample []struct {
		Root      studyPlanNode `json:"Plan"`
		Planning  float64       `json:"Planning Time"`
		Execution float64       `json:"Execution Time"`
	}
	require.NotEmpty(t, stats.exampleSQL)
	require.NoError(t, f.store.withScope(context.Background(), f.team, true, func(tx *gorm.DB, _ scope) error {
		var b []byte
		err := tx.Statement.ConnPool.QueryRowContext(tx.Statement.Context, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+stats.exampleSQL, stats.exampleArgs...).Scan(&b)
		if err != nil {
			return err
		}
		return json.Unmarshal(b, &sample)
	}))
	require.Len(t, sample, 1)
	plan := studyQueryPlan{PlanningMS: sample[0].Planning, ExecutionMS: sample[0].Execution}
	var visit func(studyPlanNode)
	visit = func(node studyPlanNode) {
		if node.Loops == 0 {
			return
		}
		if node.Relation != "" {
			plan.Relations = append(plan.Relations, node.Relation)
		}
		if node.Index != "" {
			plan.Indexes = append(plan.Indexes, node.Index)
		}
		for _, child := range node.Plans {
			visit(child)
		}
	}
	visit(sample[0].Root)
	slices.Sort(plan.Relations)
	plan.Relations = slices.Compact(plan.Relations)
	slices.Sort(plan.Indexes)
	plan.Indexes = slices.Compact(plan.Indexes)
	return plan
}
