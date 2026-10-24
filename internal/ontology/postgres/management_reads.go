package postgres

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"gorm.io/gorm"
)

func managementSource(source ontology.SourceSnapshot) ontology.ManagementSource {
	text := []rune(ontology.SourceDisplayText(source))
	truncated := len(text) > 512
	if truncated {
		text = text[:512]
	}
	return ontology.ManagementSource{SourceHandle: source.SourceHandle, Text: string(text), Truncated: truncated, OwnerID: source.OwnerID}
}

func (s *Store) ListManagementSources(ctx context.Context, teamID string, kind ontology.SourceKind, cursor, query string, limit int) (ontology.ManagementSourcePage, error) {
	page := ontology.ManagementSourcePage{Sources: []ontology.ManagementSource{}}
	if limit < 1 || limit > ontology.MaxPageSize || utf8.RuneCountInString(query) > 128 {
		return page, ontology.ErrInvalid
	}
	if cursor != "" && kind != ontology.PredicateSource {
		if id, err := uuid.Parse(cursor); err != nil || id.String() != cursor {
			return page, ontology.ErrInvalid
		}
	}
	if len(cursor) > 128 {
		return page, ontology.ErrInvalid
	}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		handles, err := maintenanceSourcePage(tx, fence, kind, cursor, limit+1)
		if err != nil {
			return err
		}
		if kind == ontology.PredicateSource && query != "" {
			handle := ontology.SourceHandle{Kind: kind}
			lookupErr := tx.Raw(eligiblePredicateSelect+` AND definition.predicate_key=? ORDER BY definition.predicate_key,definition.version DESC LIMIT 1`, fence.TeamID, fence.SpaceID, fence.Generation, query).Row().Scan(&handle.ID, &handle.Version)
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
			if lookupErr == nil {
				handles = []ontology.SourceHandle{handle}
			}
		}
		if parsed, parseErr := uuid.Parse(query); parseErr == nil && parsed.String() == query && kind != ontology.PredicateSource {
			handle, lookupErr := latestMaintenanceHandle(tx, fence, ontology.SourceHandle{Kind: kind, ID: query, Version: 1})
			if lookupErr != nil && !errors.Is(lookupErr, sql.ErrNoRows) {
				return lookupErr
			}
			handles = nil
			if lookupErr == nil {
				handles = []ontology.SourceHandle{handle}
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
			if query != "" && !strings.Contains(strings.ToLower(ontology.SourceDisplayText(source)), strings.ToLower(query)) && handle.ID != query {
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

func (s *Store) TeamOntologyStatus(ctx context.Context, teamID string) (ontology.TeamOntologyStatus, error) {
	result := ontology.TeamOntologyStatus{ObservedAt: time.Now().UTC()}
	err := s.withManagerScope(ctx, teamID, false, func(tx *gorm.DB, fence scope, _ string) error {
		var err error
		result.Revision, err = catalogRevision(tx, fence)
		if err != nil {
			return err
		}
		if err := tx.Raw(`SELECT count(*) FROM ontology_maintenance_markers WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.PendingMarkers); err != nil {
			return err
		}
		if err := tx.Raw(`SELECT EXISTS(SELECT 1 FROM ontology_maintenance_teams WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=? AND discovery_kind=4)`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.DiscoveryComplete); err != nil {
			return err
		}
		result.DiscoveryComplete = result.DiscoveryComplete && result.PendingMarkers == 0
		if err := tx.Raw(`SELECT count(*) FILTER(WHERE eligible),count(*) FILTER(WHERE eligible AND status='organized'),count(*) FILTER(WHERE eligible AND status='pending'),count(*) FILTER(WHERE eligible AND status='ambiguous'),count(*) FILTER(WHERE eligible AND status='failed') FROM ontology_maintenance_sources WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?`, fence.TeamID, fence.SpaceID, fence.Generation).Row().Scan(&result.Counts.Eligible, &result.Counts.Organized, &result.Counts.Pending, &result.Counts.Ambiguous, &result.Counts.Failed); err != nil {
			return err
		}
		result.CoverageComplete = result.DiscoveryComplete && result.Counts.Pending == 0 && result.Counts.Ambiguous == 0 && result.Counts.Failed == 0
		return nil
	})
	return result, err
}
