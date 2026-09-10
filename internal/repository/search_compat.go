package repository

import (
	searchmaintenance "github.com/markhuangai/dense-mem/internal/search/maintenance"
)

func searchDocumentFromNative(value searchmaintenance.SearchDocumentForEmbedding) SearchDocumentForEmbedding {
	return SearchDocumentForEmbedding{
		SearchDocumentResult: SearchDocumentResult{
			TeamID: value.TeamID, SearchDocumentID: value.SearchDocumentID, OwnerProfileID: value.OwnerProfileID,
			SourceKind: value.SourceKind, SourceID: value.SourceID, SourceVersion: value.SourceVersion,
			ProjectionFormat: value.ProjectionFormat, ProjectionGenerationID: value.ProjectionGenerationID,
			DocumentVersion: value.DocumentVersion, EmbeddingContractID: value.EmbeddingContractID,
			EmbeddingDimensions: value.EmbeddingDimensions, SearchState: value.SearchState,
			SpaceID: value.SpaceID, SpaceGeneration: value.SpaceGeneration,
		},
		DocumentText: value.DocumentText, DocumentHash: value.DocumentHash,
		StoredDocumentHash: value.StoredDocumentHash, Retired: value.Retired,
	}
}

func searchDocumentsFromNative(values []searchmaintenance.SearchDocumentForEmbedding) []SearchDocumentForEmbedding {
	if values == nil {
		return nil
	}
	result := make([]SearchDocumentForEmbedding, len(values))
	for index, value := range values {
		result[index] = searchDocumentFromNative(value)
	}
	return result
}

func searchDocumentEmbeddingToNative(value SearchDocumentEmbedding) searchmaintenance.SearchDocumentEmbedding {
	return searchmaintenance.SearchDocumentEmbedding{
		TeamID: value.TeamID, SearchDocumentID: value.SearchDocumentID, OwnerProfileID: value.OwnerProfileID,
		SourceKind: value.SourceKind, SourceID: value.SourceID, DocumentText: value.DocumentText,
		DocumentHash: value.DocumentHash, StoredDocumentHash: value.StoredDocumentHash,
		SourceVersion: value.SourceVersion, ProjectionFormat: value.ProjectionFormat,
		ProjectionGenerationID: value.ProjectionGenerationID, DocumentVersion: value.DocumentVersion,
		EmbeddingContractID: value.EmbeddingContractID, EmbeddingDimensions: value.EmbeddingDimensions,
		Embedding: append([]float32(nil), value.Embedding...), SpaceID: value.SpaceID,
		SpaceGeneration: value.SpaceGeneration, Retired: value.Retired,
	}
}

func searchApplyInput(value ApplySearchReconciliationInput) searchmaintenance.ApplySearchReconciliationInput {
	result := searchmaintenance.ApplySearchReconciliationInput{
		EmbeddingContractID: value.EmbeddingContractID, EmbeddingDimensions: value.EmbeddingDimensions,
		Documents: make([]searchmaintenance.SearchDocumentEmbedding, len(value.Documents)),
	}
	for index, document := range value.Documents {
		result.Documents[index] = searchDocumentEmbeddingToNative(document)
	}
	return result
}

func searchApplyResultFromNative(value *searchmaintenance.SearchReconciliationApplyResult) *SearchReconciliationApplyResult {
	if value == nil {
		return nil
	}
	return &SearchReconciliationApplyResult{UpdatedCount: value.UpdatedCount, SkippedCount: value.SkippedCount, RemainingDriftedCount: value.RemainingDriftedCount}
}

func searchReconciliationRunFromNative(value *searchmaintenance.SearchReconciliationRun) *SearchReconciliationRun {
	if value == nil {
		return nil
	}
	return &SearchReconciliationRun{
		RunID: value.RunID, LocalRunDate: value.LocalRunDate, Status: value.Status,
		SelectedCount: value.SelectedCount, EmbeddedCount: value.EmbeddedCount,
		UpdatedCount: value.UpdatedCount, DriftedCount: value.DriftedCount,
		LastError: value.LastError, StartedAt: value.StartedAt, CompletedAt: value.CompletedAt,
		UpdatedAt: value.UpdatedAt,
	}
}

func searchConvergenceFromNative(value *searchmaintenance.SearchConvergence) *SearchConvergence {
	if value == nil {
		return nil
	}
	result := &SearchConvergence{
		ObservedAt: value.ObservedAt, Status: value.Status, Contract: value.Contract,
		ExpectedDocuments: value.ExpectedDocuments, CurrentDocuments: value.CurrentDocuments,
		DriftedDocuments: value.DriftedDocuments, AffectedTeamCount: value.AffectedTeamCount,
		OldestDriftAge: value.OldestDriftAge, DriftClasses: make([]SearchDocumentDriftCount, len(value.DriftClasses)),
		LatestRun: searchReconciliationRunFromNative(value.LatestRun),
	}
	for index, drift := range value.DriftClasses {
		result.DriftClasses[index] = SearchDocumentDriftCount{Class: drift.Class, Count: drift.Count}
	}
	return result
}
