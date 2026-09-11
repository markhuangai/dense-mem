package serverapp

import (
	recall "github.com/markhuangai/dense-mem/internal/recall"
)

type recallFeedbackApplicationDependencies struct {
	Events   recall.RecallFeedbackEventStore
	Config   recall.RecallFeedbackRetentionProvider
	Resolver recall.RecallFeedbackResultResolver
}

func buildRecallFeedbackApplication(deps recallFeedbackApplicationDependencies) *recall.RecallFeedbackEventServiceImpl {
	return recall.NewRecallFeedbackEventService(deps.Events, deps.Config, deps.Resolver)
}
