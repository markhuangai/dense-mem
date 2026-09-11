// Package service keeps the pre-cutover feedback service names available to
// the shared composition root until issue #382 removes the final facade.
package service

import recall "github.com/markhuangai/dense-mem/internal/recall"

type (
	RecallFeedbackEventReader       = recall.RecallFeedbackEventReader
	RecallFeedbackEventRecorder     = recall.RecallFeedbackEventRecorder
	RecallFeedbackEventService      = recall.RecallFeedbackEventService
	RecallFeedbackRetentionProvider = recall.RecallFeedbackRetentionProvider
	RecallFeedbackResultResolver    = recall.RecallFeedbackResultResolver
	RecallFeedbackEventServiceImpl  = recall.RecallFeedbackEventServiceImpl
)

var (
	ErrRecallFeedbackInvalidInput     = recall.ErrRecallFeedbackInvalidInput
	ErrRecallFeedbackInvalidResultRef = recall.ErrRecallFeedbackInvalidResultRef
)

func NewRecallFeedbackEventService(
	repo recall.RecallFeedbackEventStore,
	retention RecallFeedbackRetentionProvider,
	resolver RecallFeedbackResultResolver,
) *RecallFeedbackEventServiceImpl {
	return recall.NewRecallFeedbackEventService(repo, retention, resolver)
}
