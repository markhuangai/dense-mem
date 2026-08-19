package response

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// SuccessEnvelope is the JSON envelope for successful responses.
type SuccessEnvelope struct {
	Data interface{} `json:"data"`
}

// Pagination describes one bounded result page.
type Pagination struct {
	Limit  int   `json:"limit"`
	Offset int   `json:"offset"`
	Total  int64 `json:"total"`
}

// PaginatedEnvelope is the JSON envelope for paginated success responses.
type PaginatedEnvelope struct {
	Data       interface{} `json:"data"`
	Pagination Pagination  `json:"pagination"`
}

// Success writes a successful response with the given status code and data.
// The response body will be: { "data": <data> }
func Success(c echo.Context, status int, data interface{}) error {
	return c.JSON(status, SuccessEnvelope{Data: data})
}

// SuccessOK writes a 200 OK response with the given data.
func SuccessOK(c echo.Context, data interface{}) error {
	return Success(c, http.StatusOK, data)
}

// PaginatedOK writes a 200 OK response with pagination metadata.
func PaginatedOK(c echo.Context, data interface{}, pagination Pagination) error {
	return c.JSON(http.StatusOK, PaginatedEnvelope{Data: data, Pagination: pagination})
}

// SuccessCreated writes a 201 Created response with the given data.
func SuccessCreated(c echo.Context, data interface{}) error {
	return Success(c, http.StatusCreated, data)
}

// SuccessNoContent writes a 204 No Content response.
func SuccessNoContent(c echo.Context) error {
	return c.NoContent(http.StatusNoContent)
}
