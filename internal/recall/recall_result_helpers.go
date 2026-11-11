package recall

import (
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	"strings"
)

const recallConflictPositionLimit = 10

func recallEvidenceConflictSummaries(records []recallcontract.EvidenceConflictCaseRecord) []RecallConflictSummary {
	out := make([]RecallConflictSummary, 0, len(records))
	for _, record := range records {
		positionsTruncated := len(record.Positions) > recallConflictPositionLimit
		positionRecords := record.Positions
		if positionsTruncated {
			positionRecords = positionRecords[:recallConflictPositionLimit]
		}
		positions := make([]RecallConflictPosition, 0, len(positionRecords))
		for _, position := range positionRecords {
			positions = append(positions, RecallConflictPosition{
				PositionID: position.PositionID, Disposition: "candidate",
				EvidenceID: position.CanonicalEvidenceID, OccurrenceID: position.OccurrenceID,
				Quote: position.Quote, SpanStart: position.SpanStart, SpanEnd: position.SpanEnd,
				Authority: position.Authority, Submitted: position.Submitted,
			})
		}
		preferred := strings.TrimSpace(record.PreferredPositionID)
		for index := range positions {
			if preferred != "" && positions[index].PositionID == preferred {
				positions[index].Disposition = "preferred"
			}
		}
		out = append(out, RecallConflictSummary{
			ConflictID: record.ConflictID, Version: record.Version, Kind: "evidence_conflict",
			Status: record.Status, PreferredPositionID: preferred, Positions: positions,
			PositionsTruncated: positionsTruncated,
		})
	}
	return out
}

func limitRecallConflictSummaries(values []RecallConflictSummary, limit int) []RecallConflictSummary {
	result := []RecallConflictSummary{}
	seen := map[string]bool{}
	for _, value := range values {
		if seen[value.ConflictID] || len(result) >= limit {
			continue
		}
		seen[value.ConflictID] = true
		result = append(result, value)
	}
	return result
}
