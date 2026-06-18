package response

import (
	"net/http"

	"github.com/labstack/echo/v4"
)

// SuccessEnvelope is the JSON envelope for successful responses.
type SuccessEnvelope struct {
	Data interface{} `json:"data"`
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

// SuccessCreated writes a 201 Created response with the given data.
func SuccessCreated(c echo.Context, data interface{}) error {
	return Success(c, http.StatusCreated, data)
}

// SuccessNoContent writes a 204 No Content response.
func SuccessNoContent(c echo.Context) error {
	return c.NoContent(http.StatusNoContent)
}
