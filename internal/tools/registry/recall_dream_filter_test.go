package registry

import (
	"testing"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/recallservice"
)

func TestRerankRelatedDreamsFiltersUnrelatedAndCaps(t *testing.T) {
	hits := []recallservice.RecallHit{
		{Fragment: &domain.Fragment{FragmentID: "fragment-hit"}, Tier: recallservice.TierFragment},
	}
	unrelated := filterDream("dream-unrelated", domain.DreamStatusProposed, []string{"dm-904"}, []domain.DreamSourceRef{{Type: "fragment", ID: "fragment-other"}}, 0.9, 0.9)
	unrelated.Hypothesis = "deployment calendar issue DM-904 may need formatting follow-up"
	dreams := []*domain.Dream{
		filterDream("dream-related-1", domain.DreamStatusProposed, []string{"dm-512", "feedback:read"}, []domain.DreamSourceRef{{Type: "fragment", ID: "fragment-hit"}}, 0.8, 0.8),
		filterDream("dream-related-2", domain.DreamStatusReinforced, []string{"dm-512"}, []domain.DreamSourceRef{{Type: "fragment", ID: "fragment-hit"}}, 0.7, 0.7),
		filterDream("dream-related-3", domain.DreamStatusProposed, []string{"feedback:read"}, nil, 0.6, 0.6),
		filterDream("dream-related-4", domain.DreamStatusProposed, []string{"dm-512"}, nil, 0.1, 0.1),
		unrelated,
		filterDream("dream-stale", domain.DreamStatusStale, []string{"dm-512"}, []domain.DreamSourceRef{{Type: "fragment", ID: "fragment-hit"}}, 0.9, 0.9),
	}

	got := rerankRelatedDreams("Which dream is related to feedback:read issue DM-512?", hits, dreams, 5)

	if len(got) != relatedDreamLimit {
		t.Fatalf("related dreams = %d; want %d", len(got), relatedDreamLimit)
	}
	gotIDs := map[string]bool{}
	for _, dream := range got {
		gotIDs[dream.DreamID] = true
	}
	for _, want := range []string{"dream-related-1", "dream-related-2", "dream-related-3"} {
		if !gotIDs[want] {
			t.Fatalf("related dreams missing %s: %+v", want, gotIDs)
		}
	}
	for _, notWant := range []string{"dream-related-4", "dream-unrelated", "dream-stale"} {
		if gotIDs[notWant] {
			t.Fatalf("related dreams included %s: %+v", notWant, gotIDs)
		}
	}
}

func filterDream(id string, status domain.DreamStatus, identifiers []string, refs []domain.DreamSourceRef, likelihood, confidence float64) *domain.Dream {
	return &domain.Dream{
		DreamID:          id,
		Hypothesis:       "feedback:read issue DM-512 may need recall filtering",
		Status:           status,
		IdentifierTokens: identifiers,
		SourceRefs:       refs,
		Likelihood:       likelihood,
		Confidence:       confidence,
	}
}
