package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

func TestRequireRole(t *testing.T) {
	cases := []struct {
		name      string
		role      string
		wantError string
	}{
		{name: "missing principal", wantError: "authentication required"},
		{name: "wrong role", role: "member", wantError: "insufficient permissions"},
		{name: "allowed role", role: "manager"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := echo.New()
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.role != "" {
				req = req.WithContext(SetPrincipalForTest(req.Context(), &Principal{
					CredentialID: testUUIDPtr(uuid.New()),
					Role:         tc.role,
				}))
			}
			rec := httptest.NewRecorder()
			c := e.NewContext(req, rec)

			called := false
			err := RequireRole("manager")(func(c echo.Context) error {
				called = true
				return c.NoContent(http.StatusNoContent)
			})(c)

			if tc.wantError != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantError) {
					t.Fatalf("error = %v; want %q", err, tc.wantError)
				}
				if called {
					t.Fatal("next handler was called")
				}
				return
			}

			if err != nil {
				t.Fatalf("RequireRole error: %v", err)
			}
			if !called {
				t.Fatal("next handler was not called")
			}
			if rec.Code != http.StatusNoContent {
				t.Fatalf("status = %d; want 204", rec.Code)
			}
		})
	}
}
