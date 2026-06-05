package response_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/http/response"
	"github.com/stretchr/testify/require"
)

func TestSuccessHelpers(t *testing.T) {
	e := echo.New()

	t.Run("SuccessOK", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx := e.NewContext(httptest.NewRequest(http.MethodGet, "/", nil), rec)

		err := response.SuccessOK(ctx, map[string]string{"id": "ok"})

		require.NoError(t, err)
		require.Equal(t, http.StatusOK, rec.Code)
		require.JSONEq(t, `{"data":{"id":"ok"}}`, rec.Body.String())
	})

	t.Run("SuccessCreated", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx := e.NewContext(httptest.NewRequest(http.MethodPost, "/", nil), rec)

		err := response.SuccessCreated(ctx, "created")

		require.NoError(t, err)
		require.Equal(t, http.StatusCreated, rec.Code)
		require.JSONEq(t, `{"data":"created"}`, rec.Body.String())
	})

	t.Run("SuccessNoContent", func(t *testing.T) {
		rec := httptest.NewRecorder()
		ctx := e.NewContext(httptest.NewRequest(http.MethodDelete, "/", nil), rec)

		err := response.SuccessNoContent(ctx)

		require.NoError(t, err)
		require.Equal(t, http.StatusNoContent, rec.Code)
		require.Empty(t, rec.Body.String())
	})
}
