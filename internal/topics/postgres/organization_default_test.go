//go:build !integration

package postgres

import "testing"

func TestOntologyOrganizationAtomicReplayAndIsolation(t *testing.T) {
	testOntologyOrganizationAtomicReplayAndIsolation(t)
}

func TestOntologyOrganizationRejectsStaleAndMalformedPublication(t *testing.T) {
	testOntologyOrganizationRejectsStaleAndMalformedPublication(t)
}

func TestOntologyOrganizationOverridesAndIncompleteGroups(t *testing.T) {
	testOntologyOrganizationOverridesAndIncompleteGroups(t)
}

func TestOntologyOrganizationRejectsSplitGroupIdentityConflict(t *testing.T) {
	testOntologyOrganizationRejectsSplitGroupIdentityConflict(t)
}

func TestOntologyOrganizationVocabularyAndSourceFences(t *testing.T) {
	testOntologyOrganizationVocabularyAndSourceFences(t)
}

func TestOntologyOrganizationAmbiguousComparisonReceipt(t *testing.T) {
	testOntologyOrganizationAmbiguousComparisonReceipt(t)
}

func TestOntologyOrganizationCompletedReceiptInvalidation(t *testing.T) {
	testOntologyOrganizationCompletedReceiptInvalidation(t)
}

func TestOntologyOrganizationCancellationReceipt(t *testing.T) {
	testOntologyOrganizationCancellationReceipt(t)
}

func TestOntologyOrganizationCohort(t *testing.T) { testOntologyOrganizationCohort(t) }
