package contract

import (
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func ApplyConflictKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	rewound := false
	if record.ResolvedAt != nil && record.ResolvedAt.After(*knownAt) {
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.RelationshipConflictOpen)
		} else {
			record.Status = string(domain.RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
		record.DismissedAt = nil
		rewound = true
	}
	if record.Status == string(domain.RelationshipConflictOverdue) && knownAt.Before(record.ReviewDueAt) {
		record.Status = string(domain.RelationshipConflictOpen)
		rewound = true
	}
	if record.Status == string(domain.RelationshipConflictDismissed) && conflictDismissedAfterKnownAt(record, knownAt) {
		record.DismissedAt = nil
		if record.ResolvedAt != nil && !record.ResolvedAt.After(*knownAt) {
			record.Status = string(domain.RelationshipConflictResolved)
			rewound = true
			applyConflictKnownAtNextReview(record, knownAt, rewound)
			return
		}
		if knownAt.Before(record.ReviewDueAt) {
			record.Status = string(domain.RelationshipConflictOpen)
		} else {
			record.Status = string(domain.RelationshipConflictOverdue)
		}
		record.PreferredPositionID = ""
		record.ResolvedAt = nil
		record.EffectiveAt = nil
		record.EffectiveTimeBasis = ""
		record.ResolutionReason = ""
		rewound = true
	}
	applyConflictKnownAtNextReview(record, knownAt, rewound)
}

func applyConflictKnownAtNextReview(record *RelationshipConflictCaseRecord, knownAt *time.Time, rewound bool) {
	if record == nil || knownAt == nil || !rewound {
		return
	}
	record.NextReviewAt = time.Time{}
}

func ApplyConflictPositionKnownAtDispositions(record *RelationshipConflictCaseRecord, knownAt *time.Time) {
	if record == nil || knownAt == nil {
		return
	}
	switch record.Status {
	case string(domain.RelationshipConflictResolved):
		for i := range record.Positions {
			if record.Positions[i].PositionID == record.PreferredPositionID {
				record.Positions[i].Disposition = string(domain.RelationshipConflictPositionPreferred)
			} else {
				record.Positions[i].Disposition = string(domain.RelationshipConflictPositionSuppressedCurrent)
			}
		}
	case string(domain.RelationshipConflictOpen), string(domain.RelationshipConflictOverdue):
		for i := range record.Positions {
			record.Positions[i].Disposition = string(domain.RelationshipConflictPositionCandidate)
		}
	}
}

func conflictDismissedAfterKnownAt(record *RelationshipConflictCaseRecord, knownAt *time.Time) bool {
	if record == nil || knownAt == nil {
		return false
	}
	if record.DismissedAt != nil {
		return record.DismissedAt.After(*knownAt)
	}
	return record.UpdatedAt.After(*knownAt)
}
