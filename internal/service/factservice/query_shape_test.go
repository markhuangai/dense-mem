package factservice

import (
	"strings"
	"testing"
)

func TestFactQueriesComputeAuthorityStateFromOverlays(t *testing.T) {
	queries := map[string]string{
		"get":        getFactCypher,
		"batch_get":  getFactsByIDCypher,
		"list":       listFactsCypher,
		"findActive": findActiveFactsCypher,
	}

	for name, query := range queries {
		t.Run(name, func(t *testing.T) {
			if strings.Contains(query, "f.authority_state") {
				t.Fatalf("%s query must not read stored f.authority_state", name)
			}
			if name == "findActive" {
				if !strings.Contains(query, "'authoritative'                  AS authority_state") {
					t.Fatalf("%s query must return a stable default authority_state", name)
				}
				return
			}
			if !strings.Contains(query, "OVERLAYS") {
				t.Fatalf("%s query must compute authority_state from OVERLAYS relationships", name)
			}
			if !strings.Contains(query, "incoming_overlay_count") || !strings.Contains(query, "outgoing_overlay_count") {
				t.Fatalf("%s query must count incoming and outgoing overlays", name)
			}
			if !strings.Contains(query, "incomingOverlay:Fact {team_id: $profileId, status: 'active'}") {
				t.Fatalf("%s query must count only active incoming overlay facts", name)
			}
			if !strings.Contains(query, "outgoingOverlay:Fact {team_id: $profileId, status: 'active'}") {
				t.Fatalf("%s query must count only active outgoing overlay facts", name)
			}
		})
	}
}
