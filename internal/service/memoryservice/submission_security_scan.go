package memoryservice

import (
	"github.com/markhuangai/dense-mem/internal/repository"
	rememberapp "github.com/markhuangai/dense-mem/internal/service/remember"
)

// The scanner now belongs to Remember intake. These aliases keep the legacy
// worker and lifecycle packages source-compatible while they are migrated.
const (
	SubmissionSecurityErrorEncodedEvidence = rememberapp.SubmissionSecurityErrorEncodedEvidence
	SubmissionSecurityErrorRejected        = rememberapp.SubmissionSecurityErrorRejected

	submissionSecuritySourceEvidence = rememberapp.SecuritySourceEvidence
	submissionSecuritySourceProposal = rememberapp.SecuritySourceProposal
)

var (
	ErrEncodedEvidenceNotAllowed = rememberapp.ErrEncodedEvidenceNotAllowed
	ErrEvidenceSecurityRejected  = rememberapp.ErrEvidenceSecurityRejected
)

type SubmissionSecurityError = rememberapp.SubmissionSecurityError
type SubmissionSecuritySignal = rememberapp.SubmissionSecuritySignal
type SubmissionSecurityScan = rememberapp.SubmissionSecurityScan
type SubmissionSecurityBatchSignal = rememberapp.SubmissionSecurityBatchSignal
type SubmissionSecurityBatchScan = rememberapp.SubmissionSecurityBatchScan

func ScanSubmissionEvidence(content string) (SubmissionSecurityScan, error) {
	return rememberapp.ScanSubmissionEvidence(content)
}

func ScanSubmissionBatch(contents []string) (SubmissionSecurityBatchScan, error) {
	return rememberapp.ScanSubmissionBatch(contents)
}

func scanSubmissionWithProviderProposal(contents []string, proposal map[string]any) (SubmissionSecurityBatchScan, error) {
	return rememberapp.ScanSubmissionWithProviderProposal(contents, proposal)
}

func submissionSecurityPassEvent() repository.SecurityEventDraft {
	return repositorySecurityEvent(rememberapp.SubmissionSecurityPassEvent())
}

func submissionSecurityQuarantineEvent(scan SubmissionSecurityScan) repository.SecurityEventDraft {
	return submissionSecurityQuarantineEventForSignals(scan.Signals, scan.SignalsTruncated, nil)
}

func submissionSecurityBatchQuarantineEvent(scan SubmissionSecurityBatchScan) repository.SecurityEventDraft {
	signals := make([]SubmissionSecuritySignal, 0, len(scan.Signals))
	sources := make([]string, 0, len(scan.Signals))
	for _, signal := range scan.Signals {
		signals = append(signals, signal.SubmissionSecuritySignal)
		sources = append(sources, signal.Source)
	}
	return submissionSecurityQuarantineEventForSignals(signals, scan.SignalsTruncated, sources)
}

func submissionSecurityQuarantineEventForSignals(
	scanSignals []SubmissionSecuritySignal,
	signalsTruncated bool,
	sources []string,
) repository.SecurityEventDraft {
	return repositorySecurityEvent(rememberapp.SubmissionSecurityQuarantineEventForSignals(scanSignals, signalsTruncated, sources))
}

func repositorySecurityEvent(event rememberapp.SecurityEventDraft) repository.SecurityEventDraft {
	converted := repository.SecurityEventDraft{
		EventKind: event.EventKind,
		Decision:  event.Decision,
		Reason:    event.Reason,
		Metadata:  event.Metadata,
	}
	for _, signal := range event.Signals {
		converted.Signals = append(converted.Signals, repository.SecuritySignalInput{
			Kind: signal.Kind, Severity: signal.Severity, SpanStart: signal.SpanStart,
			SpanEnd: signal.SpanEnd, Metadata: signal.Metadata,
		})
	}
	return converted
}
