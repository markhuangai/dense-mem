package service

import (
	"context"
	"fmt"

	"github.com/markhuangai/dense-mem/internal/storage/neo4j"
)

// ProfileDataPurger removes non-Postgres profile-owned state.
type ProfileDataPurger interface {
	PurgeProfileData(ctx context.Context, profileID string) error
}

type neo4jProfileDataPurger struct {
	writer neo4j.ScopedWriter
}

// NewNeo4jProfileDataPurger creates a profile data purger for Neo4j graph rows.
func NewNeo4jProfileDataPurger(writer neo4j.ScopedWriter) ProfileDataPurger {
	return &neo4jProfileDataPurger{writer: writer}
}

func (p *neo4jProfileDataPurger) PurgeProfileData(ctx context.Context, profileID string) error {
	if p == nil || p.writer == nil {
		return nil
	}

	if _, err := p.writer.ScopedWrite(ctx, profileID, `
		MATCH ()-[r]-()
		WHERE r.team_id = $profileId
		DELETE r
	`, nil); err != nil {
		return fmt.Errorf("purge profile relationships: %w", err)
	}

	if _, err := p.writer.ScopedWrite(ctx, profileID, `
		MATCH (n)
		WHERE n.team_id = $profileId
		DETACH DELETE n
	`, nil); err != nil {
		return fmt.Errorf("purge profile nodes: %w", err)
	}

	return nil
}
