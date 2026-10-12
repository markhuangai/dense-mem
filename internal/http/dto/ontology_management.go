package dto

type OntologyDraftCommandRequest struct {
	OperationKey     string `json:"operation_key" validate:"required,max=128"`
	ExpectedVersion  *int64 `json:"expected_version" validate:"required,min=1"`
	ExpectedRevision *int64 `json:"expected_revision" validate:"required,min=0"`
	Reason           string `json:"reason" validate:"required,max=512"`
}

type OntologyPreviewRequest struct {
	ExpectedVersion *int64 `json:"expected_version" validate:"required,min=1"`
}

type OntologyRollbackRequest struct {
	OperationKey     string `json:"operation_key" validate:"required,max=128"`
	ExpectedRevision *int64 `json:"expected_revision" validate:"required,min=0"`
	Reason           string `json:"reason" validate:"required,max=512"`
}
