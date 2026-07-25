package serverapp

import (
	"strings"

	"github.com/markhuangai/dense-mem/internal/config"
)

func VerifierConfigured(cfg config.ConfigProvider) bool {
	return strings.TrimSpace(cfg.GetAIVerifierAPIURL()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierAPIKey()) != "" &&
		strings.TrimSpace(cfg.GetAIReviewerModel()) != "" &&
		strings.TrimSpace(cfg.GetAIVerifierModel()) != ""
}
