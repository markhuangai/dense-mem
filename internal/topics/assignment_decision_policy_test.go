package topics

import (
	"fmt"
	"testing"

	"github.com/markhuangai/dense-mem/internal/modelprovider"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	"github.com/stretchr/testify/require"
)

func TestDecisionPolicyV1LabeledAssignments(t *testing.T) {
	require.Equal(t, "topic-assignment-v1", assignment.PolicyVersion)
	topics := []assignment.Topic{
		{ID: "postgres", Version: 2, Label: "PostgreSQL"},
		{ID: "recovery", Version: 1, Label: "Disaster recovery"},
		{ID: "sharding", Version: 5, Label: "Database sharding"},
	}
	for _, label := range []struct {
		evidence    string
		scores      [3]float64
		uncovered   float64
		assignments []string
		needsModel  bool
	}{
		{"Restore PostgreSQL to a standby region after an outage.", [3]float64{.99, .997, .01}, .005, []string{"postgres", "recovery"}, false},
		{"Change the PostgreSQL statement timeout for reporting jobs.", [3]float64{.993, .01, .005}, .01, []string{"postgres"}, false},
		{"Partition customers by tenant ID across database shards.", [3]float64{.01, .001, .995}, .008, []string{"sharding"}, false},
		{"Restore PostgreSQL, then rotate the exposed database credentials.", [3]float64{.999, .997, .007}, .995, nil, true},
		{"Confirm whether the PostgreSQL replicas share a failure domain.", [3]float64{.997, .73, .003}, .008, nil, true},
		{"Call me when this is done.", [3]float64{.004, .003, .01}, .002, nil, true},
		{"Change PostgreSQL's autovacuum threshold.", [3]float64{.989, .002, .002}, .008, nil, true},
		{"Change PostgreSQL's autovacuum threshold.", [3]float64{.995, .002, .006}, .011, nil, true},
	} {
		t.Run(label.evidence, func(t *testing.T) {
			answers := map[string]modelprovider.DecisionAnswer{
				"uncovered": {Type: modelprovider.DecisionPredicate, Probability: &label.uncovered},
			}
			for index, score := range label.scores {
				answers[fmt.Sprintf("match_%d", index)] = modelprovider.DecisionAnswer{Type: modelprovider.DecisionPredicate, Probability: &score}
			}
			matches, clear := clearDecisionAssignment(topics, answers)
			require.Equal(t, !label.needsModel, clear)
			if clear {
				var assigned []string
				for _, match := range matches {
					assigned = append(assigned, match.ID)
				}
				require.Equal(t, label.assignments, assigned)
			}
		})
	}
}
