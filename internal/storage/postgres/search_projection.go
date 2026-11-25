package postgres

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"

	knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"
)

const EvidenceSearchProjectionFormat = 1

func SearchDocumentHash(text string) string {
	hash := sha256.Sum256([]byte(strings.TrimSpace(text)))
	return hex.EncodeToString(hash[:])
}

const CanonicalSearchEvidenceQuery = `
			FROM evidence_fragments AS fragment
			JOIN teams AS team
			  ON team.id = fragment.team_id
			 AND team.status = 'active'
			 AND team.deleted_at IS NULL
			JOIN knowledge_ingests AS ingest
			  ON ingest.team_id = fragment.team_id
			 AND ingest.ingest_id = fragment.ingest_id
			 AND ingest.owner_profile_id = fragment.owner_profile_id
			LEFT JOIN evidence_sources AS source
			  ON source.team_id = fragment.team_id
			 AND source.source_id = fragment.source_id
			WHERE fragment.space_generation = dense_mem_active_space_generation(fragment.team_id, fragment.space_id)
			  AND (fragment.source_id IS NULL OR source.current_revision_id = fragment.source_revision_id)
			  AND NOT EXISTS (
			      SELECT 1 FROM evidence_quarantines AS quarantine
			      WHERE quarantine.team_id = fragment.team_id
			        AND quarantine.fragment_id = fragment.fragment_id
			        AND quarantine.status = 'active'
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM evidence_lifecycle_events AS lifecycle
			      WHERE lifecycle.team_id = fragment.team_id
			        AND lifecycle.target_fragment_id = fragment.fragment_id
			  )
			  AND NOT EXISTS (
			      SELECT 1 FROM evidence_exact_aliases AS alias
			      WHERE alias.team_id = fragment.team_id
			        AND alias.alias_fragment_id = fragment.fragment_id
			  )
			  AND COALESCE(fragment.metadata->>'conflict_resolution_deletion_only', '') <> 'true'`

func CanonicalEvidenceSearchDocument(document knowledgecontract.SearchDocumentForEmbedding, content, spaceID string, spaceGeneration int64) knowledgecontract.SearchDocumentForEmbedding {
	document.SourceVersion = 1
	document.ProjectionFormat = EvidenceSearchProjectionFormat
	document.ProjectionGenerationID = ""
	document.DocumentText = strings.TrimSpace(content)
	document.DocumentHash = SearchDocumentHash(document.DocumentText)
	document.SpaceID = strings.TrimSpace(spaceID)
	document.SpaceGeneration = spaceGeneration
	return document
}

func SearchDocumentMatchesCanonical(document, expected knowledgecontract.SearchDocumentForEmbedding) bool {
	return document.SourceVersion == expected.SourceVersion &&
		document.ProjectionFormat == expected.ProjectionFormat &&
		strings.TrimSpace(document.ProjectionGenerationID) == strings.TrimSpace(expected.ProjectionGenerationID) &&
		document.DocumentText == expected.DocumentText &&
		document.DocumentHash == expected.DocumentHash &&
		strings.TrimSpace(document.SpaceID) == strings.TrimSpace(expected.SpaceID) &&
		document.SpaceGeneration == expected.SpaceGeneration
}
