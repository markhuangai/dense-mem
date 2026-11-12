package service

import (
	remembercontract "github.com/markhuangai/dense-mem/internal/remember/contract"
	remember "github.com/markhuangai/dense-mem/internal/remember/service"
	session "github.com/markhuangai/dense-mem/internal/session/contract"
	"unicode/utf8"
)

func buildSessionAssessment(submission *session.Submission, responses []session.ExtractionResponse) ([]remembercontract.EvidenceInput, error) {
	excerpts, err := session.SelectedExcerpts(submission, responses)
	if err != nil {
		return nil, err
	}
	sourceGroup := "session:" + session.IdentityHash(submission.Intake.Request, "")
	source := submission.Intake.Request.Framework + ":" + submission.Intake.Request.AppName
	if utf8.RuneCountInString(source) > 256 {
		source = sourceGroup
	}
	evidence := []remember.RememberEvidenceInput{}
	for _, excerpt := range excerpts {
		event := submission.Intake.Request.Events[excerpt.EventIndex]
		provenance := map[string]any{
			"framework": submission.Intake.Request.Framework, "app_name": submission.Intake.Request.AppName,
			"user_id": submission.Intake.Request.UserID, "session_id": submission.Intake.Request.SessionID,
			"event_id": event.EventID, "event_index": excerpt.EventIndex, "span_start": excerpt.Start, "span_end": excerpt.End,
		}
		if event.OccurredAt != nil {
			provenance["occurred_at"] = *event.OccurredAt
		}
		evidence = append(evidence, remember.RememberEvidenceInput{
			Content: excerpt.Content, SourceType: "conversation", Source: source,
			SourceGroup: sourceGroup, Authority: "primary", Metadata: map[string]any{"session": provenance},
		})
	}
	return remember.RepositoryEvidenceInputs(evidence), nil
}
