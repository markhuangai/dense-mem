package factservice

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service/fragmentcodec"
)

func splitFactsByOwner(facts []*domain.Fact, ownerProfileID string) (owned, foreign []*domain.Fact) {
	for _, fact := range facts {
		if factOwnerID(fact) == ownerProfileID {
			owned = append(owned, fact)
		} else {
			foreign = append(foreign, fact)
		}
	}
	return owned, foreign
}

func factOwnerID(fact *domain.Fact) string {
	if fact == nil {
		return ""
	}
	return firstNonEmpty(fact.OwnerProfileID, fact.CreatedByProfileID, fact.PromotedByProfileID)
}

// rowToClaimForPromote maps a Neo4j result row to the minimal domain.Claim
// fields required for promotion. profileID is propagated from the caller
// rather than read from the row because ScopedRead has already enforced scope.
func rowToClaimForPromote(profileID string, row map[string]any) *domain.Claim {
	strVal := func(key string) string {
		v, _ := row[key].(string)
		return v
	}
	float64Val := func(key string) float64 {
		v, _ := row[key].(float64)
		return v
	}
	timePtr := func(key string) *time.Time {
		v, ok := row[key].(time.Time)
		if !ok {
			return nil
		}
		return &v
	}
	timeVal := func(key string) time.Time {
		v, _ := row[key].(time.Time)
		return v
	}
	stringSlice := func(key string) []string {
		raw, ok := row[key].([]any)
		if !ok {
			return nil
		}
		out := make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}

	var supportedBy []string
	if raw, ok := row["supported_by"].([]any); ok {
		supportedBy = make([]string, 0, len(raw))
		for _, v := range raw {
			if s, ok := v.(string); ok && s != "" {
				supportedBy = append(supportedBy, s)
			}
		}
	}

	var classification map[string]any
	if decoded := fragmentcodec.DecodeOptionalMap(row["classification"]); decoded != nil {
		classification = decoded
	} else if decoded := fragmentcodec.DecodeOptionalMap(row["classification_json"]); decoded != nil {
		classification = decoded
	}

	return &domain.Claim{
		ClaimID:                      strVal("claim_id"),
		ProfileID:                    profileID,
		OwnerProfileID:               firstNonEmpty(strVal("owner_profile_id"), strVal("created_by_profile_id")),
		OwnerProfileName:             firstNonEmpty(strVal("owner_profile_name"), strVal("created_by_profile_name")),
		CreatedByProfileID:           strVal("created_by_profile_id"),
		CreatedByProfileName:         strVal("created_by_profile_name"),
		Subject:                      strVal("subject"),
		Predicate:                    strVal("predicate"),
		Object:                       strVal("object"),
		Modality:                     domain.ClaimModality(strVal("modality")),
		Status:                       domain.ClaimStatus(strVal("status")),
		EntailmentVerdict:            domain.EntailmentVerdict(strVal("entailment_verdict")),
		RecordedAt:                   timeVal("recorded_at"),
		VerifiedAt:                   timePtr("verified_at"),
		ExtractConf:                  float64Val("extract_conf"),
		ResolutionConf:               float64Val("resolution_conf"),
		SourceQuality:                float64Val("source_quality"),
		ValidFrom:                    timePtr("valid_from"),
		ValidTo:                      timePtr("valid_to"),
		Classification:               classification,
		ClassificationLatticeVersion: strVal("classification_lattice_version"),
		RecallText:                   strVal("recall_text"),
		IdentifierTokens:             stringSlice("identifier_tokens"),
		SupportedBy:                  supportedBy,
	}
}
