package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	topic "github.com/markhuangai/dense-mem/internal/topics/contract"
	"gorm.io/gorm"
)

func managementSource(source topic.SourceSnapshot) topic.ManagementSource {
	text := []rune(topic.SourceDisplayText(source))
	truncated := len(text) > 512
	if truncated {
		text = text[:512]
	}
	return topic.ManagementSource{SourceHandle: source.SourceHandle, Text: string(text), Truncated: truncated, OwnerID: source.OwnerID}
}

func (s *Store) ListManagementSources(ctx context.Context, teamID string, kind topic.SourceKind, cursor, query string, limit int) (topic.ManagementSourcePage, error) {
	page := topic.ManagementSourcePage{Sources: []topic.ManagementSource{}}
	if limit < 1 || limit > topic.MaxPageSize || utf8.RuneCountInString(query) > 128 {
		return page, topic.ErrInvalid
	}
	if cursor != "" {
		if id, err := uuid.Parse(cursor); err != nil || id.String() != cursor {
			return page, topic.ErrInvalid
		}
	}
	if len(cursor) > 128 {
		return page, topic.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		handles, err := maintenanceSourcePage(tx, fence, kind, cursor, limit+1)
		if err != nil {
			return err
		}
		if parsed, parseErr := uuid.Parse(query); parseErr == nil && parsed.String() == query {
			handle, lookupErr := latestMaintenanceHandle(tx, fence, topic.SourceHandle{Kind: kind, ID: query, Version: 1})
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
			handles = nil
			if lookupErr == nil {
				handles = []topic.SourceHandle{handle}
			}
		}
		more := len(handles) > limit
		if more {
			handles = handles[:limit]
		}
		snapshots, err := readSources(tx, fence, handles)
		if err != nil {
			return err
		}
		for _, handle := range handles {
			source, ok := snapshots[handle]
			if !ok || !source.Eligible {
				continue
			}
			item := managementSource(source)
			if query != "" && !strings.Contains(strings.ToLower(topic.SourceDisplayText(source)), strings.ToLower(query)) && handle.ID != query {
				continue
			}
			page.Sources = append(page.Sources, item)
		}
		if more {
			page.NextCursor = handles[len(handles)-1].ID
		}
		page.Revision, err = catalogRevision(tx, fence)
		return err
	})
	return page, err
}

func (s *Store) TeamTopicStatus(ctx context.Context, teamID string) (topic.TeamTopicStatus, error) {
	result := topic.TeamTopicStatus{ObservedAt: time.Now().UTC()}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		var err error
		result.Revision, err = catalogRevision(tx, fence)
		if err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM topic_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.PendingMarkers); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM topic_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND discovery_kind=1)`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.DiscoveryComplete); err != nil {
			return err
		}
		result.DiscoveryComplete = result.DiscoveryComplete && result.PendingMarkers == 0
		if err := tx.Raw(`SELECT count(*) FILTER(WHERE eligible),count(*) FILTER(WHERE eligible AND status='organized'),count(*) FILTER(WHERE eligible AND status='pending'),count(*) FILTER(WHERE eligible AND status='ambiguous'),count(*) FILTER(WHERE eligible AND status='failed'),count(*) FILTER(WHERE eligible AND status='uncategorized'),count(*) FILTER(WHERE eligible AND equivalence_status='pending'),count(*) FILTER(WHERE eligible AND equivalence_status='completed'),count(*) FILTER(WHERE eligible AND equivalence_status='failed') FROM topic_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.Counts.Eligible, &result.Counts.Organized, &result.Counts.Pending, &result.Counts.Ambiguous, &result.Counts.Failed, &result.Counts.Uncategorized, &result.Equivalence.Pending, &result.Equivalence.Completed, &result.Equivalence.Failed); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FILTER(WHERE current.placement='placed'),count(*) FILTER(WHERE current.placement='standalone'),
			count(*) FILTER(WHERE current.placement='declared' AND latest.status IS DISTINCT FROM 'failed' AND `+communityEligible+`),
			count(*) FILTER(WHERE latest.status='failed' AND `+communityEligible+`)
			FROM topics AS current `+latestCommunityOutcome+`
			WHERE current.team_id=?::uuid AND current.shared_space_id=?::uuid AND current.space_generation=? AND NOT current.retired`,
			fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.Communities.Placed, &result.Communities.Standalone, &result.Communities.Pending, &result.Communities.Failed); err != nil {
			return err
		}
		result.CoverageComplete = result.DiscoveryComplete && result.Counts.Pending == 0 && result.Counts.Ambiguous == 0 && result.Counts.Failed == 0 && result.Equivalence.Pending == 0 && result.Equivalence.Failed == 0 && result.Communities.Pending == 0 && result.Communities.Failed == 0
		return nil
	})
	return result, err
}
