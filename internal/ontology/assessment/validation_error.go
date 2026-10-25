package assessment

import ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"

type validationError struct {
	cause      error
	diagnostic ontology.AssessmentValidation
}

func (e *validationError) Error() string { return e.cause.Error() }
func (e *validationError) Unwrap() error { return e.cause }

func validationFailure(stage, code, family string, cause error) *validationError {
	return &validationError{cause: cause, diagnostic: ontology.AssessmentValidation{Stage: stage, Code: code, FieldFamily: family}}
}

func validationCountFailure(stage, code, family string, expected, actual int, cause error) *validationError {
	err := validationFailure(stage, code, family, cause)
	err.diagnostic.ExpectedCount = &expected
	err.diagnostic.ActualCount = &actual
	return err
}
