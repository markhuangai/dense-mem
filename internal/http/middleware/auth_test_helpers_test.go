package middleware

import (
	"github.com/labstack/echo/v4"
	"github.com/markhuangai/dense-mem/internal/crypto"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func testAuthOptions(validator SSOEntitlementValidator) AuthOptions {
	return AuthOptions{
		CredentialVerifier:       crypto.NewArgon2Verifier(0),
		CredentialLookupPrefixes: crypto.GetLookupPrefixes,
		SSOEntitlementValidator:  validator,
	}
}

func testAuthMiddleware(repo accessservice.CredentialStore, auditSvc accessservice.AuditService) echo.MiddlewareFunc {
	return AuthMiddlewareWithOptions(repo, auditSvc, nil, testAuthOptions(nil))
}
