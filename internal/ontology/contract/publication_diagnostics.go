package contract

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
)

type PublicationRejection struct {
	Cause    error  `json:"-"`
	Reason   string `json:"reason"`
	RecordID string `json:"record_id,omitempty"`
	ParentID string `json:"parent_id,omitempty"`
	Expected string `json:"expected,omitempty"`
	Observed string `json:"observed,omitempty"`
}

func (e *PublicationRejection) Error() string { return e.Cause.Error() }
func (e *PublicationRejection) Unwrap() error { return e.Cause }

func PublicationVersionRejection(reason, id string, expected, observed int64) error {
	return &PublicationRejection{Cause: ErrConflict, Reason: reason, RecordID: id, Expected: strconv.FormatInt(expected, 10), Observed: strconv.FormatInt(observed, 10)}
}

type PublicationDiagnostic struct {
	OperationID      string
	TeamID           string
	Action           string
	Management       bool
	DraftID          string
	RollbackOf       string
	Draft            *ManagementDraft
	Preview          *ImpactPreview
	Input            json.RawMessage
	InputState       string
	InputBytes       int
	InputReason      string
	Publication      *Publication
	ObservedRevision *int64
	Definitions      map[string]Record
	DefinitionsState string
	Result           *PublicationResult
	AssessmentID     string
	ProviderIdentity string
	Duration         time.Duration
	Rejection        *PublicationRejection
	Failure          error
}

type PublicationObserver func(context.Context, PublicationDiagnostic)

type publicationDiagnosticKey struct{}

func PublicationDiagnosticFromContext(ctx context.Context) *PublicationDiagnostic {
	value, _ := ctx.Value(publicationDiagnosticKey{}).(*PublicationDiagnostic)
	return value
}

// ObservePublication shares one transient observation across nested publication owners and emits only after the outer transaction returns.
func ObservePublication(ctx context.Context, observer PublicationObserver, teamID, action string, input any) (context.Context, *PublicationDiagnostic, func(error)) {
	if current := PublicationDiagnosticFromContext(ctx); current != nil {
		return ctx, current, func(error) {}
	}
	if observer == nil {
		return ctx, nil, func(error) {}
	}
	started := time.Now()
	value := &PublicationDiagnostic{OperationID: uuid.NewString(), TeamID: teamID, Action: action, InputState: "captured"}
	body, err := json.Marshal(input)
	value.InputBytes = len(body)
	if err != nil {
		value.InputState, value.InputReason = "unavailable", "formatting_failed"
	} else if len(body) > MaxPublicationBytes {
		value.InputState, value.InputReason = "unavailable", "publication_bound"
	} else {
		value.Input = body
	}
	ctx = context.WithValue(ctx, publicationDiagnosticKey{}, value)
	return ctx, value, func(err error) {
		value.Duration, value.Failure = time.Since(started), err
		if err != nil {
			errors.As(err, &value.Rejection)
			value.Result = nil
			value.Preview = nil
		}
		observer(ctx, *value)
	}
}
