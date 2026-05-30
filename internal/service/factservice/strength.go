package factservice

import (
	"math"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// Strength is the multi-dimensional evidence weight of a Claim or Fact.
//
// The four-element comparison vector (SupportCountGateMet, MaxSourceQualityGateMet,
// ResolutionConf, ExtractConf) determines whether two Strength values are
// comparable. Two strengths are comparable iff their gate booleans are equal
// and their float components are within 1e-6 of each other.
//
// TruthScore is an aggregate [0,1] score derived as:
//
//	0.35*ExtractConf + 0.35*ResolutionConf + 0.15*bool(SupportCountGateMet) + 0.15*bool(MaxSourceQualityGateMet)
//
// For Facts, TruthScore is the value stored at promotion time (the individual
// confidence values are not retained on a Fact).
type Strength struct {
	// Comparison vector — gate outcomes
	SupportCountGateMet     bool
	MaxSourceQualityGateMet bool

	// Comparison vector — confidence signals
	ResolutionConf float64
	ExtractConf    float64

	// Aggregate score
	TruthScore float64
}

// StrengthComparison is the result of CompareStrength.
type StrengthComparison int

const (
	// StrengthEqual means both values have equal TruthScore (within 1e-6).
	StrengthEqual StrengthComparison = 0
	// StrengthAGreater means the first argument has a higher TruthScore.
	StrengthAGreater StrengthComparison = 1
	// StrengthBGreater means the second argument has a higher TruthScore.
	StrengthBGreater StrengthComparison = -1
	// StrengthIncomparable means the comparison vectors differ beyond epsilon;
	// no ordering can be determined.
	StrengthIncomparable StrengthComparison = 2
)

const strengthEpsilon = 1e-6

// boolWeight converts a boolean gate outcome to its numeric weight.
func boolWeight(b bool) float64 {
	if b {
		return 1.0
	}
	return 0.0
}

// computeTruthScore derives the weighted truth score from the four comparison
// vector components.
//
//	score = 0.35*extract + 0.35*resolution + 0.15*bool(support) + 0.15*bool(maxQuality)
func computeTruthScore(extractConf, resolutionConf float64, supportCountGateMet, maxSourceQualityGateMet bool) float64 {
	return 0.35*extractConf +
		0.35*resolutionConf +
		0.15*boolWeight(supportCountGateMet) +
		0.15*boolWeight(maxSourceQualityGateMet)
}

func validConfidence(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// ClaimStrength computes the Strength of a Claim evaluated against the given
// PromotionGate.
//
// Support gate semantics follow OR logic (AC-35): the claim passes the support
// check when EITHER len(SupportedBy) >= gate.MinSourceCount OR
// claim.SourceQuality >= gate.MinMaxSourceQuality.
func ClaimStrength(claim *domain.Claim, gate PromotionGate) Strength {
	supportCountGateMet := len(claim.SupportedBy) >= gate.MinSourceCount
	maxSourceQualityGateMet := gate.MinMaxSourceQuality > 0 && claim.SourceQuality >= gate.MinMaxSourceQuality

	return Strength{
		SupportCountGateMet:     supportCountGateMet,
		MaxSourceQualityGateMet: maxSourceQualityGateMet,
		ResolutionConf:          claim.ResolutionConf,
		ExtractConf:             claim.ExtractConf,
		TruthScore: computeTruthScore(
			claim.ExtractConf,
			claim.ResolutionConf,
			supportCountGateMet,
			maxSourceQualityGateMet,
		),
	}
}

// FactStrength computes the Strength of a Fact evaluated against the given
// PromotionGate.
//
// A Fact does not retain the individual ExtractConf / ResolutionConf values
// from its promoting Claim, so those components are set to zero and the stored
// Fact.TruthScore is used directly. Gate booleans are derived from
// Fact.SourceQuality and the gate's MinMaxSourceQuality threshold; SupportCountGateMet
// is unconditionally true because a Fact must have passed the support gate during
// promotion.
//
// Setting ResolutionConf = ExtractConf = 0 for all Facts ensures that two Facts
// evaluated against the same gate are always comparable (their float comparison
// vector components are identical), enabling TruthScore-based supersession.
func FactStrength(fact *domain.Fact, gate PromotionGate) Strength {
	// A promoted Fact has already satisfied the support gate.
	const supportCountGateMet = true
	maxSourceQualityGateMet := gate.MinMaxSourceQuality > 0 && fact.SourceQuality >= gate.MinMaxSourceQuality

	return Strength{
		SupportCountGateMet:     supportCountGateMet,
		MaxSourceQualityGateMet: maxSourceQualityGateMet,
		ResolutionConf:          0,
		ExtractConf:             0,
		TruthScore:              fact.TruthScore,
	}
}

// CompareStrength compares two Strength values.
//
// Returns StrengthIncomparable when the comparison vectors differ beyond
// strengthEpsilon (1e-6) for float components or when gate booleans differ.
//
// When both values are comparable, the result is determined by TruthScore:
//   - StrengthAGreater when a.TruthScore > b.TruthScore (beyond epsilon)
//   - StrengthBGreater when b.TruthScore > a.TruthScore (beyond epsilon)
//   - StrengthEqual when the scores are within epsilon of each other
func CompareStrength(a, b Strength) StrengthComparison {
	if a.SupportCountGateMet != b.SupportCountGateMet ||
		a.MaxSourceQualityGateMet != b.MaxSourceQualityGateMet ||
		math.Abs(a.ResolutionConf-b.ResolutionConf) > strengthEpsilon ||
		math.Abs(a.ExtractConf-b.ExtractConf) > strengthEpsilon {
		return StrengthIncomparable
	}

	diff := a.TruthScore - b.TruthScore
	switch {
	case math.Abs(diff) <= strengthEpsilon:
		return StrengthEqual
	case diff > 0:
		return StrengthAGreater
	default:
		return StrengthBGreater
	}
}
