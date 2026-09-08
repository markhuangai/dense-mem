package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

const AssessmentPolicyVersion = "v2.4.confidence-gate.1"

type LoadAutoWriteConfidencePolicyInput struct {
	TeamID          string
	OwnerProfileID  string
	GlobalThreshold float64
}

type AutoWriteConfidencePolicy struct {
	Threshold     float64
	Source        string
	ConfigVersion int64
	Version       string
}

func (r *LedgerRepositoryImpl) LoadAutoWriteConfidencePolicy(
	ctx context.Context,
	input LoadAutoWriteConfidencePolicyInput,
) (AutoWriteConfidencePolicy, error) {
	input.TeamID = strings.TrimSpace(input.TeamID)
	input.OwnerProfileID = strings.TrimSpace(input.OwnerProfileID)
	if err := validateLoadAutoWriteConfidencePolicyInput(input); err != nil {
		return AutoWriteConfidencePolicy{}, err
	}
	policy := AutoWriteConfidencePolicy{
		Threshold: input.GlobalThreshold,
		Source:    "global",
		Version:   AssessmentPolicyVersion,
	}
	err := r.withTeamProfileTx(ctx, input.TeamID, input.OwnerProfileID, func(tx *gorm.DB) error {
		var configVersion int64
		var thresholdRaw []byte
		err := tx.WithContext(ctx).Raw(`
			SELECT config_version,
			       config #> '{memory_write,auto_write_confidence_threshold}'
			FROM teams
			WHERE id = ?::uuid
		`, input.TeamID).Row().Scan(&configVersion, &thresholdRaw)
		if err != nil {
			return err
		}
		policy.ConfigVersion = configVersion
		if len(thresholdRaw) == 0 || string(thresholdRaw) == "null" {
			return nil
		}
		threshold, err := confidenceThresholdFromJSON(thresholdRaw)
		if err != nil {
			return fmt.Errorf("memory_write.auto_write_confidence_threshold: %w", err)
		}
		policy.Threshold = threshold
		policy.Source = "team"
		return nil
	})
	if err != nil {
		return AutoWriteConfidencePolicy{}, fmt.Errorf("load auto-write confidence policy: %w", err)
	}
	return policy, nil
}

func validateLoadAutoWriteConfidencePolicyInput(input LoadAutoWriteConfidencePolicyInput) error {
	for label, value := range map[string]string{
		"team_id":          input.TeamID,
		"owner_profile_id": input.OwnerProfileID,
	} {
		if _, err := uuid.Parse(value); err != nil {
			return fmt.Errorf("%s is required: %w", label, err)
		}
	}
	if math.IsNaN(input.GlobalThreshold) || math.IsInf(input.GlobalThreshold, 0) || input.GlobalThreshold < 0 || input.GlobalThreshold > 1 {
		return errors.New("global threshold must be between 0 and 1")
	}
	return nil
}

func confidenceThresholdFromJSON(raw []byte) (float64, error) {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return 0, errors.New("must be a JSON number")
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, errors.New("must be a JSON number")
	}
	threshold, err := number.Float64()
	if err != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) || threshold < 0 || threshold > 1 {
		return 0, errors.New("must be between 0 and 1")
	}
	return threshold, nil
}

func jsonObject(raw []byte) bool {
	var value map[string]json.RawMessage
	return len(raw) > 0 && json.Unmarshal(raw, &value) == nil && value != nil
}
