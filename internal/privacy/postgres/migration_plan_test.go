package postgres

import (
	"encoding/json"
	"testing"

	privacy "github.com/markhuangai/dense-mem/internal/privacy/contract"
	"github.com/stretchr/testify/require"
)

func TestPrivateMigrationNodeAttribution(t *testing.T) {
	for _, kind := range []string{"entity_records", "value_records"} {
		t.Run(kind, func(t *testing.T) {
			nodeID := `"00000000-0000-0000-0000-000000000001"`
			createdAt := `"2026-10-01T00:00:00Z"`
			nodeColumn, eventTable, eventColumn := "entity_id", "entity_resolution_events", "entity_id"
			otherTable, otherColumn := "relationship_observations", "object_value_id"
			if kind == "value_records" {
				nodeColumn, eventTable, eventColumn = "value_id", "relationship_observations", "object_value_id"
				otherTable, otherColumn = "entity_resolution_events", "entity_id"
			}
			record := &privateMigrationRecord{Table: &privateMigrationTable{Name: kind}, Key: "node", SourceSpace: "source", Fields: map[string]json.RawMessage{nodeColumn: json.RawMessage(nodeID), "created_at": json.RawMessage(createdAt)}}
			event := func(table, column, space, member, id, at, action string) *privateMigrationRecord {
				return &privateMigrationRecord{Table: &privateMigrationTable{Name: table}, SourceSpace: space, Membership: member, Fields: map[string]json.RawMessage{column: json.RawMessage(id), "created_at": json.RawMessage(at), "action": json.RawMessage(action)}}
			}
			matching := func(member string) *privateMigrationRecord {
				return event(eventTable, eventColumn, "source", member, nodeID, createdAt, `"create"`)
			}
			p := &privateMigrationPlan{Report: &privacy.PrivateMemoryMigrationReport{}, Spaces: map[string]*privateMigrationSpace{
				"source":  {Writers: map[string]bool{"A": true, "B": true}},
				"foreign": {Writers: map[string]bool{"A": true, "B": true}},
			}, Records: []*privateMigrationRecord{record,
				event(eventTable, eventColumn, "foreign", "B", nodeID, createdAt, `"create"`),
				event(otherTable, otherColumn, "source", "B", nodeID, createdAt, `"create"`),
				event(eventTable, eventColumn, "source", "B", `"00000000-0000-0000-0000-000000000002"`, createdAt, `"create"`),
				event(eventTable, eventColumn, "source", "B", nodeID, `"2026-10-01T00:00:00+00:00"`, `"create"`),
				matching(""),
			}}
			if kind == "entity_records" {
				p.Records = append(p.Records, event(eventTable, eventColumn, "source", "B", nodeID, createdAt, `"reuse"`))
			}
			p.attributePrivateMigrationNodes()
			require.Empty(t, record.Membership)
			require.Equal(t, []string{"ambiguous_node_writer:" + kind + ":node"}, p.Report.Blockers)
			p.Report.Blockers = nil
			p.Records = append(p.Records, matching("A"), matching("A"))
			p.attributePrivateMigrationNodes()
			require.Equal(t, "A", record.Membership)
			require.Empty(t, p.Report.Blockers)
			p.Records = append(p.Records, matching("B"), matching("A"))
			p.attributePrivateMigrationNodes()
			require.Equal(t, []string{"ambiguous_node_writer:" + kind + ":node"}, p.Report.Blockers)
		})
	}
	for _, writers := range []map[string]bool{{}, {"sole": true}} {
		record := &privateMigrationRecord{Table: &privateMigrationTable{Name: "entity_records"}, SourceSpace: "source"}
		p := &privateMigrationPlan{Report: &privacy.PrivateMemoryMigrationReport{}, Spaces: map[string]*privateMigrationSpace{"source": {Writers: writers}}, Records: []*privateMigrationRecord{record}}
		p.attributePrivateMigrationNodes()
		require.Empty(t, p.Report.Blockers)
		if len(writers) == 0 {
			require.Empty(t, record.Membership)
		} else {
			require.Equal(t, "sole", record.Membership)
		}
	}
}
