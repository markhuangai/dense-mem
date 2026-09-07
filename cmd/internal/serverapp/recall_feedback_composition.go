package serverapp

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	"github.com/markhuangai/dense-mem/internal/service"
)

type recallFeedbackApplicationDependencies struct {
	Events   repository.RecallFeedbackEventRepository
	Config   service.RecallFeedbackRetentionProvider
	Resolver service.RecallFeedbackResultResolver
}

func buildRecallFeedbackApplication(deps recallFeedbackApplicationDependencies) *service.RecallFeedbackEventServiceImpl {
	return service.NewRecallFeedbackEventService(deps.Events, deps.Config, deps.Resolver)
}
