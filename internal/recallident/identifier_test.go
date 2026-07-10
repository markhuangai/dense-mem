package recallident

import "testing"

func TestExtractProjectIdentifiers(t *testing.T) {
	text := "PR #56 changed SearchPanel.tsx in exp/recall-typed-expired-currentness at ce7ca8f8a6ccbbfa71554c3f5d2b9a9c4d55a9c9. See eval_list_recall_feedback_events and feedback:read for v1.6.4."
	got := Extract(text)
	for _, want := range []string{
		"pr-56",
		"#56",
		"ce7ca8f8a6ccbbfa71554c3f5d2b9a9c4d55a9c9",
		"v1.6.4",
		"feedback:read",
		"exp/recall-typed-expired-currentness",
		"searchpanel.tsx",
		"eval_list_recall_feedback_events",
	} {
		if !contains(got, want) {
			t.Fatalf("Extract(%q) missing %q in %#v", text, want, got)
		}
	}
}

func TestBuildFragmentRecallTextIncludesMetadata(t *testing.T) {
	recallText, tokens := BuildFragmentRecallText(
		"Release workflow update.",
		"github/pr/60",
		"remember:release:v1.6.4",
		[]string{"release_workflow"},
		map[string]any{
			"file":       ".github/workflows/release.yml",
			"tool_names": []any{"eval_run_recall_case", "recall_memory"},
		},
	)
	for _, want := range []string{"github/pr/60", ".github/workflows/release.yml", "release_workflow", "eval_run_recall_case", "recall_memory", "v1.6.4"} {
		if !contains(tokens, want) {
			t.Fatalf("tokens missing %q in %#v", want, tokens)
		}
		if !contains(Extract(recallText), want) {
			t.Fatalf("recall_text missing %q in %q", want, recallText)
		}
	}
}

func TestExtractPRNumberMetadataKey(t *testing.T) {
	got := Extract("commit 2e4b0cc pr_number 56")

	for _, want := range []string{"pr-56", "#56", "2e4b0cc"} {
		if !contains(got, want) {
			t.Fatalf("Extract metadata key text missing %q in %#v", want, got)
		}
	}
}

func TestOverlapCountsSharedIdentifiers(t *testing.T) {
	query := Extract("What changed in PR #56 for 2e4b0cc?")
	candidate := Extract("Implementation note for PR #56.")
	if got := Overlap(query, candidate); got == 0 {
		t.Fatalf("Overlap = 0; query=%#v candidate=%#v", query, candidate)
	}
}

func TestHardGateAnchorsKeepProjectIdentifiers(t *testing.T) {
	text := "PR #56 changed SearchPanel.tsx in exp/recall-typed-expired-currentness at ce7ca8f8a6ccbbfa71554c3f5d2b9a9c4d55a9c9. See eval_list_recall_feedback_events and feedback:read for v1.6.4."
	got := HardGateAnchors(Extract(text))

	for _, want := range []string{
		"pr-56",
		"ce7ca8f8a6ccbbfa71554c3f5d2b9a9c4d55a9c9",
		"v1.6.4",
		"feedback:read",
		"exp/recall-typed-expired-currentness",
		"searchpanel.tsx",
		"eval_list_recall_feedback_events",
	} {
		if !contains(got, want) {
			t.Fatalf("HardGateAnchors missing %q in %#v", want, got)
		}
	}
}

func TestHardGateAnchorsExcludeNaturalLanguageIdentifiers(t *testing.T) {
	text := "TCR/CD3 microdomains, H2A.Z activation, Smc5/6 engagement, Fz/PCP-dependent localization, interleukin-2 response, α-MyHC/CFA induction, and singer/songwriter credits."
	tokens := Extract(text)
	got := HardGateAnchors(tokens)

	if len(tokens) == 0 {
		t.Fatal("test query did not produce identifier-like tokens")
	}
	if len(got) != 0 {
		t.Fatalf("HardGateAnchors(%#v) = %#v; want no hard gates", tokens, got)
	}
}

func TestRankingAnchorsKeepContextQualifiedProjectIdentifiersAndFilenames(t *testing.T) {
	tests := []struct {
		text string
		want string
	}{
		{text: "What timeout should job UNT-013 use?", want: "unt-013"},
		{text: "What is the deployment window for service OBS-001?", want: "obs-001"},
		{text: "What is the source of truth for issue DM-412?", want: "dm-412"},
		{text: "Which pager should queue NEG-001 use?", want: "neg-001"},
		{text: "Who owns account TMP-001?", want: "tmp-001"},
		{text: "What is thumbs.db used for?", want: "thumbs.db"},
		{text: "What does smsvchost.exe install?", want: "smsvchost.exe"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := RankingAnchors(tt.text)
			if !contains(got, tt.want) {
				t.Fatalf("RankingAnchors(%q) missing %q in %#v", tt.text, tt.want, got)
			}
		})
	}
}

func TestRankingAnchorsExcludeNaturalLanguageIdentifiers(t *testing.T) {
	tests := []string{
		"MicroRNA is involved in Neural Stem Cell differentiation.",
		"mRNAs and miRNA regulate translation.",
		"SHP-2 activates MAPK signaling.",
		"What were the effects on the U.S. economy?",
		"Which singer/songwriter was a judge?",
	}

	for _, text := range tests {
		t.Run(text, func(t *testing.T) {
			if got := RankingAnchors(text); len(got) != 0 {
				t.Fatalf("RankingAnchors(%q) = %#v; want no ranking anchors", text, got)
			}
		})
	}
}

func TestSatisfiesStrongAnchorsRequiresExactPRAndCommit(t *testing.T) {
	query := Extract("What changed in PR #56 commit 2e4b0cc for evalListEdges?")

	if !SatisfiesStrongAnchors(query, "Implementation note for PR #56 at commit 2e4b0cc.") {
		t.Fatal("required PR and commit candidate did not satisfy strong anchors")
	}
	if SatisfiesStrongAnchors(query, "Implementation note for PR #65 that mentions evalListEdges.") {
		t.Fatal("candidate with wrong PR satisfied strong anchors")
	}
	if SatisfiesStrongAnchors(query, "Abandoned note says no PR #56 fix was merged.") {
		t.Fatal("candidate missing commit satisfied strong anchors")
	}
}

func TestSatisfiesStrongAnchorsRequiresBranchAndRun(t *testing.T) {
	query := Extract("Result for branch exp/recall-typed-expired-currentness run local_eval_1k_v2")

	if !SatisfiesStrongAnchors(query, "branch exp/recall-typed-expired-currentness run local_eval_1k_v2 passed") {
		t.Fatal("required branch/run candidate did not satisfy strong anchors")
	}
	if SatisfiesStrongAnchors(query, "branch exp/recall-dream-rerank run local_eval_1k_v2 passed") {
		t.Fatal("candidate with wrong branch satisfied strong anchors")
	}
	if SatisfiesStrongAnchors(query, "branch exp/recall-typed-expired-currentness run local_train_relational_100k_v1 failed") {
		t.Fatal("candidate with wrong run satisfied strong anchors")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
