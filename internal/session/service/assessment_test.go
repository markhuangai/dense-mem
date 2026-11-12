package service

import (
	"fmt"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
	"unicode/utf8"
)

func sessionAssessmentFixture(t *testing.T, request session.Request, start, end int) (*session.Submission, []session.ExtractionResponse) {
	t.Helper()
	windows, err := BuildWindows(request, "o200k_base")
	require.NoError(t, err)
	submission := &session.Submission{ID: "submission", Intake: session.Intake{Request: request, Windows: windows, ExtractionVersion: session.ExtractionVersion}, NewEventIndices: []int{0}}
	responses := make([]session.ExtractionResponse, len(windows))
	for i, window := range windows {
		responses[i] = session.ExtractionResponse{RequestID: fmt.Sprintf("session:submission:window:%d", window.Index), Coverage: []string{}, Excerpts: []session.Excerpt{}, SecuritySignals: []session.ExtractionSignal{}}
		for _, segment := range window.Core {
			responses[i].Coverage = append(responses[i].Coverage, segment.Ref)
		}
	}
	responses[0].Excerpts = []session.Excerpt{{EventIndex: 0, Start: start, End: end, Content: string([]rune(request.Events[0].Text)[start:end])}}
	return submission, responses
}

func TestEvidenceOnlyUnitSessionAssessmentBoundsSourceWithoutChangingProvenance(t *testing.T) {
	for _, count := range []int{127, 128, 256} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			req := sessionRequest(session.Event{EventID: "one", Text: "  Ari uses Go. café 🧭\n"})
			req.Framework, req.AppName = strings.Repeat("🧭", count), strings.Repeat("é", min(count+1, 256))
			require.NoError(t, ValidateRequest(req))
			submission, responses := sessionAssessmentFixture(t, req, 0, len([]rune(req.Events[0].Text)))
			evidence, err := buildSessionAssessment(submission, responses)
			require.NoError(t, err)
			require.Len(t, evidence, 1)
			require.LessOrEqual(t, utf8.RuneCountInString(evidence[0].SourceRef), 256)
			want := req.Framework + ":" + req.AppName
			group := "session:" + session.IdentityHash(req, "")
			if count > 127 {
				want = group
			}
			require.Equal(t, want, evidence[0].SourceRef)
			require.Equal(t, group, evidence[0].Metadata["contract_source_group"])
			provenance := evidence[0].Metadata["session"].(map[string]any)
			require.Equal(t, req.Framework, provenance["framework"])
			require.Equal(t, req.AppName, provenance["app_name"])
			require.Equal(t, req.Events[0].Text, evidence[0].Content)
		})
	}
}

func TestEvidenceOnlyUnitSessionAssessmentPreservesOriginalOffsetsAndNumericText(t *testing.T) {
	for _, number := range []string{"42", "9007199254740993", "0.1234567890123456789"} {
		text := " 🧭 Ari records " + number + " ms on 2026-10-10.  "
		occurred := "2026-10-10T12:00:00Z"
		req := sessionRequest(session.Event{EventID: "one", Text: text, OccurredAt: &occurred})
		submission, responses := sessionAssessmentFixture(t, req, 1, len([]rune(text)))
		evidence, err := buildSessionAssessment(submission, responses)
		require.NoError(t, err)
		require.Len(t, evidence, 1)
		require.Equal(t, string([]rune(text)[1:]), evidence[0].Content)
		provenance := evidence[0].Metadata["session"].(map[string]any)
		require.Equal(t, 1, provenance["span_start"])
		require.Equal(t, len([]rune(text)), provenance["span_end"])
		require.Equal(t, "one", provenance["event_id"])
		require.Equal(t, occurred, provenance["occurred_at"])
		submission.Intake.Request.Events[0].Text = "changed"
		_, err = buildSessionAssessment(submission, responses)
		require.Error(t, err)
	}
}
