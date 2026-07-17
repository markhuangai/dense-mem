package registry

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const v2MemoryPackArtifactMaxLength = 1_000_000

func validateV2CorrectEntityResolution(args map[string]any) error {
	if err := validateV2UniqueStringArray(args, "selected_observation_ids"); err != nil {
		return err
	}
	action, _ := args["action"].(string)
	dryRun, _ := args["dry_run"].(bool)
	selected, _ := args["selected_observation_ids"].([]any)
	switch domain.V2EntityCorrectionAction(action) {
	case domain.V2EntityCorrectionMerge:
		if err := validateV2RequiredFields(args, "target_entity_id"); err != nil {
			return err
		}
		if len(selected) > 0 {
			return fmt.Errorf("selected_observation_ids is only accepted for action %s", domain.V2EntityCorrectionSplit)
		}
	case domain.V2EntityCorrectionSplit:
		if len(selected) == 0 {
			return fmt.Errorf("selected_observation_ids is required for action %s", action)
		}
		if value, ok := args["target_entity_id"].(string); ok && strings.TrimSpace(value) != "" {
			return fmt.Errorf("target_entity_id is only accepted for action %s", domain.V2EntityCorrectionMerge)
		}
	}
	if !dryRun {
		return validateV2RequiredFields(args, "plan_token")
	}
	return nil
}

func validateV2MemoryPackSource(args map[string]any) error {
	artifactJSON, hasArtifact := nonEmptyString(args["artifact_json"])
	rawURL, hasURL := nonEmptyString(args["url"])
	if hasArtifact == hasURL {
		return fmt.Errorf("exactly one of artifact_json or url is required")
	}
	if hasArtifact && utf8.RuneCountInString(artifactJSON) > v2MemoryPackArtifactMaxLength {
		return fmt.Errorf("artifact_json exceeds maximum length of %d", v2MemoryPackArtifactMaxLength)
	}
	if hasURL {
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("url must be an absolute HTTPS URL")
		}
	}
	return nil
}

func nonEmptyString(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	return text, text != ""
}
