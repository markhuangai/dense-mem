package dto

// CreateUserPortalSessionRequest controls whether the browser session survives
// closing the browser. The pointer distinguishes an omitted field from false.
type CreateUserPortalSessionRequest struct {
	Remember *bool `json:"remember" validate:"required"`
}
