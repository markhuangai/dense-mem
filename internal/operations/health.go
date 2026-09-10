package operations

import (
	"context"
	"fmt"
	"strings"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

func CheckSearchReadiness(ctx context.Context, search interface {
	CheckSearchReadiness(context.Context) (*searchcontract.SearchReadiness, error)
}) error {
	if search == nil {
		return fmt.Errorf("%w: search repository is required", knowledgecontract.ErrSearchContractMismatch)
	}
	readiness, err := search.CheckSearchReadiness(ctx)
	if err != nil {
		return err
	}
	if readiness == nil || readiness.Ready {
		return nil
	}
	reasons := make([]string, 0, len(readiness.Reasons))
	for _, reason := range readiness.Reasons {
		message := strings.TrimSpace(reason.Message)
		if message == "" {
			message = strings.TrimSpace(reason.Code)
		}
		if message != "" {
			reasons = append(reasons, message)
		}
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "search readiness check failed")
	}
	return fmt.Errorf("%w: %s", knowledgecontract.ErrSearchContractMismatch, strings.Join(reasons, "; "))
}
