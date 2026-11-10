package contract

import (
	"github.com/stretchr/testify/require"
	"strings"
	"testing"
)

func evidenceOnlyExcerptFixture(text string) (*Submission, []ExtractionResponse) {
	segment := Segment{Ref: "current", EventIndex: 0, Start: 0, End: len([]rune(text)), Text: text}
	submission := &Submission{ID: "submission", Intake: Intake{ExtractionVersion: ExtractionVersion, Request: Request{Events: []Event{{EventID: "current", Text: text}}}, Windows: []Window{{Index: 0, Core: []Segment{segment}}}}, NewEventIndices: []int{0}, Prior: []PriorEvent{{Event: Event{EventID: "previous", Text: "Prior context must not become Evidence."}}}}
	response := ExtractionResponse{RequestID: "session:submission:window:0", Coverage: []string{"current"}, Excerpts: []Excerpt{}, SecuritySignals: []ExtractionSignal{}}
	return submission, []ExtractionResponse{response}
}

func TestEvidenceOnlyUnitSessionExactOriginalRuneSpans(t *testing.T) {
	submission, responses := evidenceOnlyExcerptFixture(" 🧭 Café timeout is 30 seconds.")
	text := []rune(submission.Intake.Request.Events[0].Text)
	responses[0].Excerpts = []Excerpt{{EventIndex: 0, Start: 1, End: 7, Content: string(text[1:7])}, {EventIndex: 0, Start: 5, End: len(text), Content: string(text[5:])}}
	selected, err := SelectedExcerpts(submission, responses)
	require.NoError(t, err)
	require.Equal(t, []Excerpt{{EventIndex: 0, Start: 1, End: len(text), Content: string(text[1:])}}, selected)
	responses[0].Excerpts[0].Content = "Rewritten summary"
	_, err = SelectedExcerpts(submission, responses)
	require.Error(t, err)
	responses[0].Excerpts = []Excerpt{{EventIndex: 1, Start: 0, End: 5, Content: "Prior"}}
	_, err = SelectedExcerpts(submission, responses)
	require.Error(t, err)
}

func TestEvidenceOnlyUnitSessionEmptyAndCompleteWindows(t *testing.T) {
	submission, responses := evidenceOnlyExcerptFixture("Hello.")
	selected, err := SelectedExcerpts(submission, responses)
	require.NoError(t, err)
	require.Empty(t, selected)
	_, err = SelectedExcerpts(submission, nil)
	require.Error(t, err)
	responses[0].Coverage = []string{}
	_, err = SelectedExcerpts(submission, responses)
	require.Error(t, err)
	responses[0].Coverage = []string{"current"}
	responses[0].SecuritySignals = []ExtractionSignal{{SegmentRef: "current", Kind: "prompt_injection"}}
	_, err = SelectedExcerpts(submission, responses)
	require.ErrorIs(t, err, ErrSecurity)
}

func TestEvidenceOnlyUnitSessionCoalescesAndChunksWithoutChangingText(t *testing.T) {
	text := strings.Repeat("é", 1500)
	submission, responses := evidenceOnlyExcerptFixture(text)
	for _, bounds := range [][2]int{{0, 900}, {800, 1500}} {
		responses[0].Excerpts = append(responses[0].Excerpts, Excerpt{EventIndex: 0, Start: bounds[0], End: bounds[1], Content: string([]rune(text)[bounds[0]:bounds[1]])})
	}
	selected, err := SelectedExcerpts(submission, responses)
	require.NoError(t, err)
	require.Len(t, selected, 2)
	require.Equal(t, text, selected[0].Content+selected[1].Content)
	require.Equal(t, 999, selected[0].End)
	require.Equal(t, 999, selected[1].Start)
}
