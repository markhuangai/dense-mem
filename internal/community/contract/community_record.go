package contract

import "github.com/markhuangai/dense-mem/internal/domain"

func (record CommunityRecord) DomainCommunity() *domain.Community {
	return &domain.Community{
		CommunityID:      record.CommunityID,
		TeamID:           record.TeamID,
		Level:            0,
		Summary:          record.Summary,
		SummaryVersion:   record.SummaryVersion,
		MemberCount:      record.MemberCount,
		TopEntities:      append([]string(nil), record.TopEntities...),
		TopPredicates:    append([]string(nil), record.TopPredicates...),
		LastSummarizedAt: record.UpdatedAt,
	}
}
