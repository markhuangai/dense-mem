package recall

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	recallcontract "github.com/markhuangai/dense-mem/internal/recall/contract"
	searchcontract "github.com/markhuangai/dense-mem/internal/search/contract"
)

type recallSearchStub struct {
	contract           *searchcontract.ActiveSearchContract
	input              recallcontract.RecallEvidenceInput
	relationshipInput  recallcontract.RecallRelationshipsInput
	result             *recallcontract.RecallEvidenceResult
	relationshipResult *recallcontract.RecallRelationshipsResult
	relationshipCalled bool
	relationshipCalls  int
	err                error
	relationshipErr    error
}

func (s *recallSearchStub) GetActiveSearchContract(context.Context) (*searchcontract.ActiveSearchContract, error) {
	if s.contract == nil {
		return nil, errors.New("missing contract")
	}
	return s.contract, nil
}

func (s *recallSearchStub) ReadEvidenceCandidates(_ context.Context, input recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, _ int) (*recallcontract.RecallCandidateBatch, error) {
	s.input = input
	if s.err != nil {
		return nil, s.err
	}
	batch := &recallcontract.RecallCandidateBatch{SearchState: string(domain.SearchProjectionCurrent)}
	if s.result != nil {
		batch.SearchState = s.result.SearchState
		for _, hit := range s.result.Results {
			batch.TextHits = append(batch.TextHits, searchcontract.SearchHit{
				SourceKind: "evidence", SourceID: hit.EvidenceID, SearchState: hit.SearchState,
			})
		}
		if len(batch.TextHits) == 0 && (len(s.result.Conflicts) > 0 || len(s.result.EvidenceConflicts) > 0) {
			batch.TextHits = append(batch.TextHits, searchcontract.SearchHit{SourceKind: "evidence", SourceID: uuid.NewString()})
		}
	}
	return batch, nil
}

func (s *recallSearchStub) HydrateEvidence(_ context.Context, _ recallcontract.RecallEvidenceInput, _ *searchcontract.ActiveSearchContract, _ []string) (map[string]recallcontract.RecallEvidenceHit, error) {
	hits := map[string]recallcontract.RecallEvidenceHit{}
	if s.result != nil {
		for _, hit := range s.result.Results {
			hits[hit.EvidenceID] = hit
		}
	}
	return hits, nil
}

func (s *recallSearchStub) LoadRecallConflicts(_ context.Context, _ recallcontract.RecallEvidenceInput, _ []recallcontract.RecallEvidenceHit) (*recallcontract.RecallConflicts, error) {
	conflicts := &recallcontract.RecallConflicts{}
	if s.result != nil {
		conflicts.Relationships = s.result.Conflicts
		conflicts.Evidence = s.result.EvidenceConflicts
	}
	return conflicts, nil
}

func (s *recallSearchStub) ReadRelationshipCandidates(_ context.Context, input recallcontract.RecallRelationshipsInput, _ *searchcontract.ActiveSearchContract, _ int) (*recallcontract.RecallCandidateBatch, error) {
	s.relationshipCalled = true
	s.relationshipCalls++
	s.relationshipInput = input
	if s.relationshipErr != nil {
		return nil, s.relationshipErr
	}
	batch := &recallcontract.RecallCandidateBatch{SearchState: string(domain.SearchProjectionCurrent)}
	if s.relationshipResult != nil {
		batch.SearchState = s.relationshipResult.SearchState
		for _, hit := range s.relationshipResult.Results {
			batch.TextHits = append(batch.TextHits, searchcontract.SearchHit{
				SourceKind: "relationship", SourceID: hit.RelationshipID, SearchState: hit.SearchState,
			})
		}
	}
	return batch, nil
}

func (s *recallSearchStub) HydrateRelationships(_ context.Context, _ recallcontract.RecallRelationshipsInput, _ *searchcontract.ActiveSearchContract, _ []string) (map[string]recallcontract.RecallRelationshipHit, error) {
	hits := map[string]recallcontract.RecallRelationshipHit{}
	if s.relationshipResult != nil {
		for _, hit := range s.relationshipResult.Results {
			hits[hit.RelationshipID] = hit
		}
	}
	return hits, nil
}
