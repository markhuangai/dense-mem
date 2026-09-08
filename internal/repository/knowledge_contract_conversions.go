package repository

import knowledgecontract "github.com/markhuangai/dense-mem/internal/knowledge/contract"

func toKnowledgeSemanticEntityResolutionInput(input SemanticEntityResolutionInput) knowledgecontract.SemanticEntityResolutionInput {
	return knowledgecontract.SemanticEntityResolutionInput(input)
}

func toKnowledgeSemanticEntityResolutionInputs(input []SemanticEntityResolutionInput) []knowledgecontract.SemanticEntityResolutionInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SemanticEntityResolutionInput, len(input))
	for i := range input {
		result[i] = toKnowledgeSemanticEntityResolutionInput(input[i])
	}
	return result
}

func toKnowledgeSemanticPredicateCandidateInput(input *SemanticPredicateCandidateInput) *knowledgecontract.SemanticPredicateCandidateInput {
	if input == nil {
		return nil
	}
	result := knowledgecontract.SemanticPredicateCandidateInput(*input)
	return &result
}

func toKnowledgeSemanticValueInput(input *SemanticValueInput) *knowledgecontract.SemanticValueInput {
	if input == nil {
		return nil
	}
	result := knowledgecontract.SemanticValueInput(*input)
	return &result
}

func toKnowledgeSemanticCorrectionTargetInput(input *SemanticCorrectionTargetInput) *knowledgecontract.SemanticCorrectionTargetInput {
	if input == nil {
		return nil
	}
	result := knowledgecontract.SemanticCorrectionTargetInput(*input)
	return &result
}

func toKnowledgeSemanticConflictContextInput(input *SemanticConflictContextInput) *knowledgecontract.SemanticConflictContextInput {
	if input == nil {
		return nil
	}
	result := knowledgecontract.SemanticConflictContextInput(*input)
	return &result
}

func toKnowledgeSemanticRelationshipDecisionInput(input SemanticRelationshipDecisionInput) knowledgecontract.SemanticRelationshipDecisionInput {
	return knowledgecontract.SemanticRelationshipDecisionInput{
		Ref:                     input.Ref,
		SubjectRef:              input.SubjectRef,
		OriginalPredicate:       input.OriginalPredicate,
		PredicateKey:            input.PredicateKey,
		PredicateVersion:        input.PredicateVersion,
		ExactPredicateKey:       input.ExactPredicateKey,
		PredicateCandidate:      toKnowledgeSemanticPredicateCandidateInput(input.PredicateCandidate),
		ObjectRef:               input.ObjectRef,
		ObjectValue:             toKnowledgeSemanticValueInput(input.ObjectValue),
		Polarity:                input.Polarity,
		ScopeKey:                input.ScopeKey,
		ValidFrom:               input.ValidFrom,
		ValidTo:                 input.ValidTo,
		EvidenceVerdict:         input.EvidenceVerdict,
		AssessorAccepted:        input.AssessorAccepted,
		PromoteToFact:           input.PromoteToFact,
		Confidence:              input.Confidence,
		Rationale:               input.Rationale,
		Model:                   input.Model,
		ResponseHash:            input.ResponseHash,
		Support:                 toKnowledgeEvidenceSupportInput(input.Support),
		Supports:                toKnowledgeEvidenceSupportInputs(input.Supports),
		CorrectionTarget:        toKnowledgeSemanticCorrectionTargetInput(input.CorrectionTarget),
		ConflictContext:         toKnowledgeSemanticConflictContextInput(input.ConflictContext),
		ObservationMetadata:     input.ObservationMetadata,
		RelationshipMetadata:    input.RelationshipMetadata,
		AssessmentID:            input.AssessmentID,
		AssessmentPolicyVersion: input.AssessmentPolicyVersion,
		ThresholdUsed:           input.ThresholdUsed,
		GateResult:              input.GateResult,
		SuppressSupport:         input.SuppressSupport,
	}
}

func toKnowledgeSemanticRelationshipDecisionInputs(input []SemanticRelationshipDecisionInput) []knowledgecontract.SemanticRelationshipDecisionInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SemanticRelationshipDecisionInput, len(input))
	for i := range input {
		result[i] = toKnowledgeSemanticRelationshipDecisionInput(input[i])
	}
	return result
}

func toKnowledgeCommitSemanticInput(input CommitSemanticInput) knowledgecontract.CommitSemanticInput {
	return knowledgecontract.CommitSemanticInput{
		TeamID:                   input.TeamID,
		OwnerProfileID:           input.OwnerProfileID,
		IngestID:                 input.IngestID,
		FragmentID:               input.FragmentID,
		EntityResolutions:        toKnowledgeSemanticEntityResolutionInputs(input.EntityResolutions),
		RelationshipObservations: toKnowledgeSemanticRelationshipDecisionInputs(input.RelationshipObservations),
	}
}

func toKnowledgeCreateEntityInput(input CreateEntityInput) knowledgecontract.CreateEntityInput {
	return knowledgecontract.CreateEntityInput(input)
}

func fromKnowledgeEntityRecord(input *knowledgecontract.EntityRecord) *EntityRecord {
	if input == nil {
		return nil
	}
	result := EntityRecord(*input)
	return &result
}

func toKnowledgeAddEntityNameInput(input AddEntityNameInput) knowledgecontract.AddEntityNameInput {
	return knowledgecontract.AddEntityNameInput(input)
}

func toKnowledgeUpsertValueInput(input UpsertValueInput) knowledgecontract.UpsertValueInput {
	return knowledgecontract.UpsertValueInput(input)
}

func toKnowledgeEnsureSemanticPredicateCandidateInput(input EnsureSemanticPredicateCandidateInput) knowledgecontract.EnsureSemanticPredicateCandidateInput {
	return knowledgecontract.EnsureSemanticPredicateCandidateInput(input)
}

func fromKnowledgeSemanticReviewPredicateCandidate(input *knowledgecontract.SemanticReviewPredicateCandidate) *SemanticReviewPredicateCandidate {
	if input == nil {
		return nil
	}
	result := SemanticReviewPredicateCandidate(*input)
	return &result
}

func fromKnowledgeValueRecord(input *knowledgecontract.ValueRecord) *ValueRecord {
	if input == nil {
		return nil
	}
	result := ValueRecord(*input)
	return &result
}

func toKnowledgeEvidenceSupportInput(input *EvidenceSupportInput) *knowledgecontract.EvidenceSupportInput {
	if input == nil {
		return nil
	}
	result := knowledgecontract.EvidenceSupportInput(*input)
	return &result
}

func toKnowledgeEvidenceSupportInputs(input []EvidenceSupportInput) []knowledgecontract.EvidenceSupportInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.EvidenceSupportInput, len(input))
	for i := range input {
		result[i] = knowledgecontract.EvidenceSupportInput(input[i])
	}
	return result
}

func toKnowledgeApplyRelationshipDecisionInput(input ApplyRelationshipDecisionInput) knowledgecontract.ApplyRelationshipDecisionInput {
	return knowledgecontract.ApplyRelationshipDecisionInput{
		TeamID:                  input.TeamID,
		OwnerProfileID:          input.OwnerProfileID,
		IngestID:                input.IngestID,
		ProposalRef:             input.ProposalRef,
		SubjectRef:              input.SubjectRef,
		SubjectEntityID:         input.SubjectEntityID,
		OriginalPredicate:       input.OriginalPredicate,
		PredicateKey:            input.PredicateKey,
		PredicateVersion:        input.PredicateVersion,
		ObjectRef:               input.ObjectRef,
		ObjectEntityID:          input.ObjectEntityID,
		ObjectValueID:           input.ObjectValueID,
		Polarity:                input.Polarity,
		ScopeKey:                input.ScopeKey,
		ValidFrom:               input.ValidFrom,
		ValidTo:                 input.ValidTo,
		EvidenceVerdict:         input.EvidenceVerdict,
		AssessorAccepted:        input.AssessorAccepted,
		PromoteToFact:           input.PromoteToFact,
		Confidence:              input.Confidence,
		Rationale:               input.Rationale,
		Model:                   input.Model,
		ResponseHash:            input.ResponseHash,
		Support:                 toKnowledgeEvidenceSupportInput(input.Support),
		Supports:                toKnowledgeEvidenceSupportInputs(input.Supports),
		ObservationMetadata:     input.ObservationMetadata,
		RelationshipMetadata:    input.RelationshipMetadata,
		AssessmentID:            input.AssessmentID,
		AssessmentPolicyVersion: input.AssessmentPolicyVersion,
		ThresholdUsed:           input.ThresholdUsed,
		GateResult:              input.GateResult,
		SuppressSupport:         input.SuppressSupport,
	}
}

func toKnowledgeRelationshipRecord(input *RelationshipRecord) *knowledgecontract.RelationshipRecord {
	if input == nil {
		return nil
	}
	result := knowledgecontract.RelationshipRecord(*input)
	return &result
}

func fromKnowledgeRelationshipRecord(input *knowledgecontract.RelationshipRecord) *RelationshipRecord {
	if input == nil {
		return nil
	}
	result := RelationshipRecord(*input)
	return &result
}

func fromKnowledgeRelationshipDecisionResult(input *knowledgecontract.RelationshipDecisionResult) *RelationshipDecisionResult {
	if input == nil {
		return nil
	}
	return &RelationshipDecisionResult{
		Relationship:        fromKnowledgeRelationshipRecord(input.Relationship),
		ObservationID:       input.ObservationID,
		VerificationEventID: input.VerificationEventID,
		SupportID:           input.SupportID,
		SupportIDs:          input.SupportIDs,
		SupportDecisionID:   input.SupportDecisionID,
		ProposalID:          input.ProposalID,
		OwnerProfileID:      input.OwnerProfileID,
		Category:            input.Category,
		Reason:              input.Reason,
		ConfidenceGate:      input.ConfidenceGate,
		PolicyVersion:       input.PolicyVersion,
		CreatedRelationship: input.CreatedRelationship,
	}
}

func toKnowledgeRetractRelationshipInput(input RetractRelationshipInput) knowledgecontract.RetractRelationshipInput {
	return knowledgecontract.RetractRelationshipInput(input)
}

func fromKnowledgeRelationshipTransitionResult(input *knowledgecontract.RelationshipTransitionResult) *RelationshipTransitionResult {
	if input == nil {
		return nil
	}
	result := RelationshipTransitionResult(*input)
	return &result
}

func toKnowledgeApplyRelationshipSupportDecisionInput(input ApplyRelationshipSupportDecisionInput) knowledgecontract.ApplyRelationshipSupportDecisionInput {
	return knowledgecontract.ApplyRelationshipSupportDecisionInput(input)
}

func fromKnowledgeRelationshipSupportDecisionResult(input *knowledgecontract.RelationshipSupportDecisionResult) *RelationshipSupportDecisionResult {
	if input == nil {
		return nil
	}
	result := RelationshipSupportDecisionResult(*input)
	return &result
}

func toKnowledgeAppendCrossReferenceInput(input AppendCrossReferenceInput) knowledgecontract.AppendCrossReferenceInput {
	return knowledgecontract.AppendCrossReferenceInput(input)
}

func toKnowledgeCorrectRelationshipInput(input CorrectRelationshipInput) knowledgecontract.CorrectRelationshipInput {
	return knowledgecontract.CorrectRelationshipInput{
		TeamID:            input.TeamID,
		OwnerProfileID:    input.OwnerProfileID,
		Action:            input.Action,
		RelationshipID:    input.RelationshipID,
		ExpectedVersion:   input.ExpectedVersion,
		Patch:             toKnowledgeRelationshipCorrectionPatch(input.Patch),
		Supports:          toKnowledgeRelationshipCorrectionSupports(input.Supports),
		Reason:            input.Reason,
		SubmissionID:      input.SubmissionID,
		ConfirmationToken: input.ConfirmationToken,
		Selection:         knowledgecontract.RelationshipCorrectionSelection(input.Selection),
		IdempotencyKey:    input.IdempotencyKey,
	}
}

func fromKnowledgeCorrectRelationshipInput(input knowledgecontract.CorrectRelationshipInput) CorrectRelationshipInput {
	return CorrectRelationshipInput{
		TeamID:            input.TeamID,
		OwnerProfileID:    input.OwnerProfileID,
		Action:            input.Action,
		RelationshipID:    input.RelationshipID,
		ExpectedVersion:   input.ExpectedVersion,
		Patch:             fromKnowledgeRelationshipCorrectionPatch(input.Patch),
		Supports:          fromKnowledgeRelationshipCorrectionSupports(input.Supports),
		Reason:            input.Reason,
		SubmissionID:      input.SubmissionID,
		ConfirmationToken: input.ConfirmationToken,
		Selection:         RelationshipCorrectionSelection(input.Selection),
		IdempotencyKey:    input.IdempotencyKey,
	}
}

func toKnowledgeRelationshipCorrectionPatch(input RelationshipCorrectionPatch) knowledgecontract.RelationshipCorrectionPatch {
	return knowledgecontract.RelationshipCorrectionPatch{
		SubjectEntity: toKnowledgeRelationshipCorrectionEntityPatch(input.SubjectEntity),
		Predicate:     toKnowledgeRelationshipCorrectionPredicatePatch(input.Predicate),
		ObjectEntity:  toKnowledgeRelationshipCorrectionEntityPatch(input.ObjectEntity),
	}
}

func fromKnowledgeRelationshipCorrectionPatch(input knowledgecontract.RelationshipCorrectionPatch) RelationshipCorrectionPatch {
	return RelationshipCorrectionPatch{
		SubjectEntity: fromKnowledgeRelationshipCorrectionEntityPatch(input.SubjectEntity),
		Predicate:     fromKnowledgeRelationshipCorrectionPredicatePatch(input.Predicate),
		ObjectEntity:  fromKnowledgeRelationshipCorrectionEntityPatch(input.ObjectEntity),
	}
}

func toKnowledgeRelationshipCorrectionEntityPatch(input *RelationshipCorrectionEntityPatch) *knowledgecontract.RelationshipCorrectionEntityPatch {
	if input == nil {
		return nil
	}
	result := knowledgecontract.RelationshipCorrectionEntityPatch(*input)
	return &result
}

func fromKnowledgeRelationshipCorrectionEntityPatch(input *knowledgecontract.RelationshipCorrectionEntityPatch) *RelationshipCorrectionEntityPatch {
	if input == nil {
		return nil
	}
	result := RelationshipCorrectionEntityPatch(*input)
	return &result
}

func toKnowledgeRelationshipCorrectionPredicatePatch(input *RelationshipCorrectionPredicatePatch) *knowledgecontract.RelationshipCorrectionPredicatePatch {
	if input == nil {
		return nil
	}
	result := knowledgecontract.RelationshipCorrectionPredicatePatch(*input)
	return &result
}

func fromKnowledgeRelationshipCorrectionPredicatePatch(input *knowledgecontract.RelationshipCorrectionPredicatePatch) *RelationshipCorrectionPredicatePatch {
	if input == nil {
		return nil
	}
	result := RelationshipCorrectionPredicatePatch(*input)
	return &result
}

func toKnowledgeRelationshipCorrectionSupports(input []RelationshipCorrectionSupport) []knowledgecontract.RelationshipCorrectionSupport {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.RelationshipCorrectionSupport, len(input))
	for i := range input {
		result[i] = knowledgecontract.RelationshipCorrectionSupport(input[i])
	}
	return result
}

func toKnowledgeRelationshipCorrectionCandidates(input []RelationshipCorrectionCandidate) []knowledgecontract.RelationshipCorrectionCandidate {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.RelationshipCorrectionCandidate, len(input))
	for i := range input {
		result[i] = knowledgecontract.RelationshipCorrectionCandidate(input[i])
	}
	return result
}

func toKnowledgeRelationshipCorrectionSelection(input RelationshipCorrectionSelection) knowledgecontract.RelationshipCorrectionSelection {
	return knowledgecontract.RelationshipCorrectionSelection(input)
}

func fromKnowledgeRelationshipCorrectionSupports(input []knowledgecontract.RelationshipCorrectionSupport) []RelationshipCorrectionSupport {
	if input == nil {
		return nil
	}
	result := make([]RelationshipCorrectionSupport, len(input))
	for i := range input {
		result[i] = RelationshipCorrectionSupport(input[i])
	}
	return result
}

func toKnowledgeRelationshipCorrectionEmbeddings(input []RelationshipCorrectionEmbedding) []knowledgecontract.RelationshipCorrectionEmbedding {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.RelationshipCorrectionEmbedding, len(input))
	for i := range input {
		result[i] = knowledgecontract.RelationshipCorrectionEmbedding(input[i])
	}
	return result
}

func fromKnowledgeCorrectRelationshipResult(input *knowledgecontract.CorrectRelationshipResult) *CorrectRelationshipResult {
	if input == nil {
		return nil
	}
	return &CorrectRelationshipResult{
		SubmissionID:    input.SubmissionID,
		ProcessingState: input.ProcessingState,
		SearchState:     input.SearchState,
		Confirmation:    fromKnowledgeRelationshipCorrectionConfirmation(input.Confirmation),
		Correction:      fromKnowledgeRelationshipCorrectionResult(input.Correction),
		ErrorCode:       input.ErrorCode,
		ErrorMessage:    input.ErrorMessage,
	}
}

func fromKnowledgeRelationshipCorrectionConfirmation(input *knowledgecontract.RelationshipCorrectionConfirmation) *RelationshipCorrectionConfirmation {
	if input == nil {
		return nil
	}
	return &RelationshipCorrectionConfirmation{
		Token:      input.Token,
		ExpiresAt:  input.ExpiresAt,
		Candidates: fromKnowledgeRelationshipCorrectionCandidates(input.Candidates),
	}
}

func fromKnowledgeRelationshipCorrectionCandidates(input []knowledgecontract.RelationshipCorrectionCandidate) []RelationshipCorrectionCandidate {
	if input == nil {
		return nil
	}
	result := make([]RelationshipCorrectionCandidate, len(input))
	for i := range input {
		result[i] = RelationshipCorrectionCandidate(input[i])
	}
	return result
}

func fromKnowledgeRelationshipCorrectionResult(input *knowledgecontract.RelationshipCorrectionResult) *RelationshipCorrectionResult {
	if input == nil {
		return nil
	}
	result := RelationshipCorrectionResult(*input)
	return &result
}

func toKnowledgeGetRelationshipCorrectionInput(input GetRelationshipCorrectionInput) knowledgecontract.GetRelationshipCorrectionInput {
	return knowledgecontract.GetRelationshipCorrectionInput(input)
}

func fromKnowledgeRelationshipCorrectionEmbeddingPlan(input *knowledgecontract.RelationshipCorrectionEmbeddingPlan) *RelationshipCorrectionEmbeddingPlan {
	if input == nil {
		return nil
	}
	return &RelationshipCorrectionEmbeddingPlan{
		Documents:               fromKnowledgeRelationshipCorrectionEmbeddingDocuments(input.Documents),
		EmbeddingContractID:     input.EmbeddingContractID,
		EmbeddingDimensions:     input.EmbeddingDimensions,
		EmbeddingModel:          input.EmbeddingModel,
		SearchIndexGenerationID: input.SearchIndexGenerationID,
		IndexGeneration:         input.IndexGeneration,
	}
}

func fromKnowledgeRelationshipCorrectionEmbeddingDocuments(input []knowledgecontract.RelationshipCorrectionEmbeddingDocument) []RelationshipCorrectionEmbeddingDocument {
	if input == nil {
		return nil
	}
	result := make([]RelationshipCorrectionEmbeddingDocument, len(input))
	for i := range input {
		result[i] = RelationshipCorrectionEmbeddingDocument(input[i])
	}
	return result
}

func toKnowledgeSubmissionAssessmentKnownEvidence(input SubmissionAssessmentKnownEvidence) knowledgecontract.SubmissionAssessmentKnownEvidence {
	return knowledgecontract.SubmissionAssessmentKnownEvidence(input)
}

func toKnowledgeSubmissionAssessmentKnownEvidenceList(input []SubmissionAssessmentKnownEvidence) []knowledgecontract.SubmissionAssessmentKnownEvidence {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SubmissionAssessmentKnownEvidence, len(input))
	for i := range input {
		result[i] = toKnowledgeSubmissionAssessmentKnownEvidence(input[i])
	}
	return result
}

func fromKnowledgeSubmissionAssessmentKnownEvidence(input knowledgecontract.SubmissionAssessmentKnownEvidence) SubmissionAssessmentKnownEvidence {
	return SubmissionAssessmentKnownEvidence(input)
}

func fromKnowledgeSubmissionAssessmentKnownEvidenceList(input []knowledgecontract.SubmissionAssessmentKnownEvidence) []SubmissionAssessmentKnownEvidence {
	if input == nil {
		return nil
	}
	result := make([]SubmissionAssessmentKnownEvidence, len(input))
	for i := range input {
		result[i] = fromKnowledgeSubmissionAssessmentKnownEvidence(input[i])
	}
	return result
}

func toKnowledgeSubmissionAssessmentEntityResolutionInput(input SubmissionAssessmentEntityResolutionInput) knowledgecontract.SubmissionAssessmentEntityResolutionInput {
	return knowledgecontract.SubmissionAssessmentEntityResolutionInput{Resolution: toKnowledgeSemanticEntityResolutionInput(input.Resolution)}
}

func toKnowledgeSubmissionAssessmentEntityResolutionInputs(input []SubmissionAssessmentEntityResolutionInput) []knowledgecontract.SubmissionAssessmentEntityResolutionInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SubmissionAssessmentEntityResolutionInput, len(input))
	for i := range input {
		result[i] = toKnowledgeSubmissionAssessmentEntityResolutionInput(input[i])
	}
	return result
}

func toKnowledgeSubmissionAssessmentRelationshipObservationInput(input SubmissionAssessmentRelationshipObservationInput) knowledgecontract.SubmissionAssessmentRelationshipObservationInput {
	return knowledgecontract.SubmissionAssessmentRelationshipObservationInput{
		RelationshipRef: input.RelationshipRef,
		SplitIndex:      input.SplitIndex,
		Observation:     toKnowledgeSemanticRelationshipDecisionInput(input.Observation),
	}
}

func toKnowledgeSubmissionAssessmentRelationshipObservationInputs(input []SubmissionAssessmentRelationshipObservationInput) []knowledgecontract.SubmissionAssessmentRelationshipObservationInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SubmissionAssessmentRelationshipObservationInput, len(input))
	for i := range input {
		result[i] = toKnowledgeSubmissionAssessmentRelationshipObservationInput(input[i])
	}
	return result
}

func toKnowledgeSubmissionAssessmentItemInputs(input []SubmissionAssessmentItemInput) []knowledgecontract.SubmissionAssessmentItemInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SubmissionAssessmentItemInput, len(input))
	for i := range input {
		result[i] = knowledgecontract.SubmissionAssessmentItemInput(input[i])
	}
	return result
}

func toKnowledgeEvidenceConflictResultInput(input EvidenceConflictResultInput) knowledgecontract.EvidenceConflictResultInput {
	result := knowledgecontract.EvidenceConflictResultInput{}
	if input.Positions != nil {
		result.Positions = make([]knowledgecontract.EvidenceConflictPositionInput, len(input.Positions))
		for i := range input.Positions {
			result.Positions[i] = knowledgecontract.EvidenceConflictPositionInput(input.Positions[i])
		}
	}
	return result
}

func toKnowledgeEvidenceConflictResultInputs(input []EvidenceConflictResultInput) []knowledgecontract.EvidenceConflictResultInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.EvidenceConflictResultInput, len(input))
	for i := range input {
		result[i] = toKnowledgeEvidenceConflictResultInput(input[i])
	}
	return result
}

func toKnowledgeSubmissionPredicateRegistrationInputs(input []SubmissionPredicateRegistrationInput) []knowledgecontract.SubmissionPredicateRegistrationInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SubmissionPredicateRegistrationInput, len(input))
	for i := range input {
		result[i] = knowledgecontract.SubmissionPredicateRegistrationInput(input[i])
	}
	return result
}

func toKnowledgeSubmissionRelationshipResultInputs(input []SubmissionRelationshipResultInput) []knowledgecontract.SubmissionRelationshipResultInput {
	if input == nil {
		return nil
	}
	result := make([]knowledgecontract.SubmissionRelationshipResultInput, len(input))
	for i := range input {
		result[i] = toKnowledgeSubmissionRelationshipResultInput(input[i])
	}
	return result
}

func toKnowledgeSubmissionRelationshipResultInput(input SubmissionRelationshipResultInput) knowledgecontract.SubmissionRelationshipResultInput {
	result := knowledgecontract.SubmissionRelationshipResultInput{
		RelationshipRef: input.RelationshipRef,
		Disposition:     input.Disposition,
		Reason:          input.Reason,
	}
	if input.Splits != nil {
		result.Splits = make([]knowledgecontract.SubmissionRelationshipSplitInput, len(input.Splits))
		for i := range input.Splits {
			result.Splits[i] = knowledgecontract.SubmissionRelationshipSplitInput(input.Splits[i])
		}
	}
	return result
}

func fromKnowledgeEvidenceConflictResultInput(input knowledgecontract.EvidenceConflictResultInput) EvidenceConflictResultInput {
	result := EvidenceConflictResultInput{}
	if input.Positions != nil {
		result.Positions = make([]EvidenceConflictPositionInput, len(input.Positions))
		for i := range input.Positions {
			result.Positions[i] = EvidenceConflictPositionInput(input.Positions[i])
		}
	}
	return result
}

func fromKnowledgeSubmissionRelationshipResultInput(input knowledgecontract.SubmissionRelationshipResultInput) SubmissionRelationshipResultInput {
	result := SubmissionRelationshipResultInput{
		RelationshipRef: input.RelationshipRef,
		Disposition:     input.Disposition,
		Reason:          input.Reason,
	}
	if input.Splits != nil {
		result.Splits = make([]SubmissionRelationshipSplitInput, len(input.Splits))
		for i := range input.Splits {
			result.Splits[i] = SubmissionRelationshipSplitInput(input.Splits[i])
		}
	}
	return result
}

func toKnowledgeCommitSubmissionAssessmentInput(input CommitSubmissionAssessmentInput) knowledgecontract.CommitSubmissionAssessmentInput {
	return knowledgecontract.CommitSubmissionAssessmentInput{
		RememberCommitScope:                  knowledgecontract.RememberCommitScope(input.RememberCommitScope),
		AssessmentID:                         input.AssessmentID,
		Items:                                toKnowledgeSubmissionAssessmentItemInputs(input.Items),
		KnownEvidenceSnapshot:                toKnowledgeSubmissionAssessmentKnownEvidenceList(input.KnownEvidenceSnapshot),
		EvidenceConflictResults:              toKnowledgeEvidenceConflictResultInputs(input.EvidenceConflictResults),
		EvidenceConflictCandidateEvidenceIDs: input.EvidenceConflictCandidateEvidenceIDs,
		EntityResolutions:                    toKnowledgeSubmissionAssessmentEntityResolutionInputs(input.EntityResolutions),
		RelationshipObservations:             toKnowledgeSubmissionAssessmentRelationshipObservationInputs(input.RelationshipObservations),
		PredicateRegistrations:               toKnowledgeSubmissionPredicateRegistrationInputs(input.PredicateRegistrations),
		RelationshipResults:                  toKnowledgeSubmissionRelationshipResultInputs(input.RelationshipResults),
		Payload:                              input.Payload,
	}
}

func fromKnowledgeSubmissionAssessmentEntityResolutionInput(input knowledgecontract.SubmissionAssessmentEntityResolutionInput) SubmissionAssessmentEntityResolutionInput {
	return SubmissionAssessmentEntityResolutionInput{Resolution: SemanticEntityResolutionInput(input.Resolution)}
}

func fromKnowledgeSubmissionAssessmentEntityResolutionInputs(input []knowledgecontract.SubmissionAssessmentEntityResolutionInput) []SubmissionAssessmentEntityResolutionInput {
	if input == nil {
		return nil
	}
	result := make([]SubmissionAssessmentEntityResolutionInput, len(input))
	for i := range input {
		result[i] = fromKnowledgeSubmissionAssessmentEntityResolutionInput(input[i])
	}
	return result
}

func fromKnowledgeSubmissionAssessmentRelationshipObservationInput(input knowledgecontract.SubmissionAssessmentRelationshipObservationInput) SubmissionAssessmentRelationshipObservationInput {
	return SubmissionAssessmentRelationshipObservationInput{
		RelationshipRef: input.RelationshipRef,
		SplitIndex:      input.SplitIndex,
		Observation:     fromKnowledgeSemanticRelationshipDecisionInput(input.Observation),
	}
}

func fromKnowledgeSemanticRelationshipDecisionInput(input knowledgecontract.SemanticRelationshipDecisionInput) SemanticRelationshipDecisionInput {
	result := SemanticRelationshipDecisionInput{
		Ref:                     input.Ref,
		SubjectRef:              input.SubjectRef,
		OriginalPredicate:       input.OriginalPredicate,
		PredicateKey:            input.PredicateKey,
		PredicateVersion:        input.PredicateVersion,
		ExactPredicateKey:       input.ExactPredicateKey,
		ObjectRef:               input.ObjectRef,
		Polarity:                input.Polarity,
		ScopeKey:                input.ScopeKey,
		ValidFrom:               input.ValidFrom,
		ValidTo:                 input.ValidTo,
		EvidenceVerdict:         input.EvidenceVerdict,
		AssessorAccepted:        input.AssessorAccepted,
		PromoteToFact:           input.PromoteToFact,
		Confidence:              input.Confidence,
		Rationale:               input.Rationale,
		Model:                   input.Model,
		ResponseHash:            input.ResponseHash,
		ObservationMetadata:     input.ObservationMetadata,
		RelationshipMetadata:    input.RelationshipMetadata,
		AssessmentID:            input.AssessmentID,
		AssessmentPolicyVersion: input.AssessmentPolicyVersion,
		ThresholdUsed:           input.ThresholdUsed,
		GateResult:              input.GateResult,
		SuppressSupport:         input.SuppressSupport,
	}
	if input.PredicateCandidate != nil {
		candidate := SemanticPredicateCandidateInput(*input.PredicateCandidate)
		result.PredicateCandidate = &candidate
	}
	if input.ObjectValue != nil {
		value := SemanticValueInput(*input.ObjectValue)
		result.ObjectValue = &value
	}
	if input.CorrectionTarget != nil {
		target := SemanticCorrectionTargetInput(*input.CorrectionTarget)
		result.CorrectionTarget = &target
	}
	if input.ConflictContext != nil {
		conflict := SemanticConflictContextInput(*input.ConflictContext)
		result.ConflictContext = &conflict
	}
	if input.Support != nil {
		support := EvidenceSupportInput(*input.Support)
		result.Support = &support
	}
	if input.Supports != nil {
		result.Supports = make([]EvidenceSupportInput, len(input.Supports))
		for i := range input.Supports {
			result.Supports[i] = EvidenceSupportInput(input.Supports[i])
		}
	}
	return result
}

func fromKnowledgeSubmissionAssessmentRelationshipObservationInputs(input []knowledgecontract.SubmissionAssessmentRelationshipObservationInput) []SubmissionAssessmentRelationshipObservationInput {
	if input == nil {
		return nil
	}
	result := make([]SubmissionAssessmentRelationshipObservationInput, len(input))
	for i := range input {
		result[i] = fromKnowledgeSubmissionAssessmentRelationshipObservationInput(input[i])
	}
	return result
}

func fromKnowledgeCommitSubmissionAssessmentInput(input knowledgecontract.CommitSubmissionAssessmentInput) CommitSubmissionAssessmentInput {
	result := CommitSubmissionAssessmentInput{
		RememberCommitScope:                  RememberCommitScope(input.RememberCommitScope),
		AssessmentID:                         input.AssessmentID,
		EvidenceConflictCandidateEvidenceIDs: input.EvidenceConflictCandidateEvidenceIDs,
		Payload:                              input.Payload,
	}
	if input.Items != nil {
		result.Items = make([]SubmissionAssessmentItemInput, len(input.Items))
		for i := range input.Items {
			result.Items[i] = SubmissionAssessmentItemInput(input.Items[i])
		}
	}
	result.KnownEvidenceSnapshot = fromKnowledgeSubmissionAssessmentKnownEvidenceList(input.KnownEvidenceSnapshot)
	if input.EvidenceConflictResults != nil {
		result.EvidenceConflictResults = make([]EvidenceConflictResultInput, len(input.EvidenceConflictResults))
		for i := range input.EvidenceConflictResults {
			result.EvidenceConflictResults[i] = fromKnowledgeEvidenceConflictResultInput(input.EvidenceConflictResults[i])
		}
	}
	result.EntityResolutions = fromKnowledgeSubmissionAssessmentEntityResolutionInputs(input.EntityResolutions)
	result.RelationshipObservations = fromKnowledgeSubmissionAssessmentRelationshipObservationInputs(input.RelationshipObservations)
	if input.PredicateRegistrations != nil {
		result.PredicateRegistrations = make([]SubmissionPredicateRegistrationInput, len(input.PredicateRegistrations))
		for i := range input.PredicateRegistrations {
			result.PredicateRegistrations[i] = SubmissionPredicateRegistrationInput(input.PredicateRegistrations[i])
		}
	}
	if input.RelationshipResults != nil {
		result.RelationshipResults = make([]SubmissionRelationshipResultInput, len(input.RelationshipResults))
		for i := range input.RelationshipResults {
			result.RelationshipResults[i] = fromKnowledgeSubmissionRelationshipResultInput(input.RelationshipResults[i])
		}
	}
	return result
}

func toKnowledgeSynchronousRememberCommitInput(input SynchronousRememberCommitInput) knowledgecontract.SynchronousRememberCommitInput {
	var securityResults []knowledgecontract.EvidenceSecurityResult
	if input.EvidenceSecurityResults != nil {
		securityResults = make([]knowledgecontract.EvidenceSecurityResult, len(input.EvidenceSecurityResults))
		for i := range input.EvidenceSecurityResults {
			item := input.EvidenceSecurityResults[i]
			securityResults[i] = knowledgecontract.EvidenceSecurityResult{
				FragmentID:    item.FragmentID,
				EvidenceID:    item.EvidenceID,
				EvidenceIndex: item.EvidenceIndex,
				Decision:      item.Decision,
				Safe:          item.Safe,
			}
			if item.Signals != nil {
				securityResults[i].Signals = make([]knowledgecontract.SecuritySignalInput, len(item.Signals))
				for j := range item.Signals {
					securityResults[i].Signals[j] = knowledgecontract.SecuritySignalInput(item.Signals[j])
				}
			}
		}
	}
	return knowledgecontract.SynchronousRememberCommitInput{
		TeamID:                                  input.TeamID,
		OwnerProfileID:                          input.OwnerProfileID,
		IngestID:                                input.IngestID,
		SpaceID:                                 input.SpaceID,
		SpaceGeneration:                         input.SpaceGeneration,
		IdempotencyKey:                          input.IdempotencyKey,
		RequestHash:                             input.RequestHash,
		SourceSummary:                           input.SourceSummary,
		Proposal:                                input.Proposal,
		Metadata:                                input.Metadata,
		Evidence:                                input.Evidence,
		AssessmentID:                            input.AssessmentID,
		AssessmentJSON:                          input.AssessmentJSON,
		EvidenceSecurityResults:                 securityResults,
		ProviderTurns:                           input.ProviderTurns,
		InputTokens:                             input.InputTokens,
		OutputTokens:                            input.OutputTokens,
		CandidateContextOmittedCandidates:       input.CandidateContextOmittedCandidates,
		CandidateContextOmittedPredicateOptions: input.CandidateContextOmittedPredicateOptions,
		AssessorTurns:                           input.AssessorTurns,
		Duration:                                input.Duration,
		StartedAt:                               input.StartedAt,
		CorrelationID:                           input.CorrelationID,
		PublicResult:                            input.PublicResult,
		DuplicateResolutions:                    input.DuplicateResolutions,
		Commit:                                  toKnowledgeCommitSubmissionAssessmentInput(input.Commit),
	}
}

func fromKnowledgeSynchronousRememberCommitInput(input knowledgecontract.SynchronousRememberCommitInput) SynchronousRememberCommitInput {
	result := SynchronousRememberCommitInput{
		TeamID:                                  input.TeamID,
		OwnerProfileID:                          input.OwnerProfileID,
		IngestID:                                input.IngestID,
		SpaceID:                                 input.SpaceID,
		SpaceGeneration:                         input.SpaceGeneration,
		IdempotencyKey:                          input.IdempotencyKey,
		RequestHash:                             input.RequestHash,
		SourceSummary:                           input.SourceSummary,
		Proposal:                                input.Proposal,
		Metadata:                                input.Metadata,
		Evidence:                                input.Evidence,
		AssessmentID:                            input.AssessmentID,
		AssessmentJSON:                          input.AssessmentJSON,
		ProviderTurns:                           input.ProviderTurns,
		InputTokens:                             input.InputTokens,
		OutputTokens:                            input.OutputTokens,
		CandidateContextOmittedCandidates:       input.CandidateContextOmittedCandidates,
		CandidateContextOmittedPredicateOptions: input.CandidateContextOmittedPredicateOptions,
		AssessorTurns:                           input.AssessorTurns,
		Duration:                                input.Duration,
		StartedAt:                               input.StartedAt,
		CorrelationID:                           input.CorrelationID,
		PublicResult:                            input.PublicResult,
		DuplicateResolutions:                    input.DuplicateResolutions,
		Commit:                                  fromKnowledgeCommitSubmissionAssessmentInput(input.Commit),
	}
	if input.EvidenceSecurityResults != nil {
		result.EvidenceSecurityResults = make([]EvidenceSecurityResult, len(input.EvidenceSecurityResults))
		for i, item := range input.EvidenceSecurityResults {
			result.EvidenceSecurityResults[i] = EvidenceSecurityResult{
				FragmentID:    item.FragmentID,
				EvidenceID:    item.EvidenceID,
				EvidenceIndex: item.EvidenceIndex,
				Decision:      item.Decision,
				Safe:          item.Safe,
			}
			if item.Signals != nil {
				result.EvidenceSecurityResults[i].Signals = make([]SecuritySignalInput, len(item.Signals))
				for j, signal := range item.Signals {
					result.EvidenceSecurityResults[i].Signals[j] = SecuritySignalInput(signal)
				}
			}
		}
	}
	return result
}
