package dto

type PrivateMemoryErasureRequest struct {
	AcknowledgeIrreversible *bool `json:"acknowledge_irreversible" validate:"required"`
}

type PrivateMemoryLegalHoldRequest struct {
	ReasonCode string `json:"reason_code" validate:"required,max=64"`
}
