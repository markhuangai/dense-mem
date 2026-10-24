package modelprovider

// CallDiagnostics contains measured durations and closed server-owned phases; nil durations were not observed.
type CallDiagnostics struct {
	QueueMS              *int64 `json:"queue_ms,omitempty"`
	AdmissionMS          *int64 `json:"admission_ms,omitempty"`
	HTTPMS               *int64 `json:"http_ms,omitempty"`
	ReconciliationMS     *int64 `json:"reconciliation_ms,omitempty"`
	HTTPTimeoutMS        *int64 `json:"http_timeout_ms,omitempty"`
	FailurePhase         string `json:"failure_phase,omitempty"`
	ReconciliationFailed bool   `json:"reconciliation_failed,omitempty"`
}

func (d *CallDiagnostics) Valid() bool {
	if d == nil {
		return true
	}
	for _, value := range []*int64{d.QueueMS, d.AdmissionMS, d.HTTPMS, d.ReconciliationMS, d.HTTPTimeoutMS} {
		if value != nil && *value < 0 {
			return false
		}
	}
	switch d.FailurePhase {
	case "", "queue", "prepare", "admission", "request", "response_body", "response_status", "response_decode", "validation", "reconciliation":
		return !d.ReconciliationFailed || d.ReconciliationMS != nil
	default:
		return false
	}
}
