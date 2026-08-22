package service

// This file is the compatibility seam for callers that have not yet moved to
// internal/service/access. The access package owns policy and ports; these
// aliases keep the staged refactor source-compatible for the remaining
// application and transport packages.

import (
	"log/slog"
	"time"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

type TeamService = accessservice.TeamService
type TeamServiceImpl = accessservice.TeamServiceImpl
type CreateTeamRequest = accessservice.CreateTeamRequest
type UpdateTeamRequest = accessservice.UpdateTeamRequest
type TeamStatePurger = accessservice.TeamStatePurger
type TeamStore = accessservice.TeamStore

func NewTeamService(repo TeamStore, auditService AuditService, statePurger TeamStatePurger) *TeamServiceImpl {
	return accessservice.NewTeamService(repo, auditService, statePurger)
}

func NewTeamServiceWithLogger(repo TeamStore, auditService AuditService, statePurger TeamStatePurger, logger *slog.Logger) *TeamServiceImpl {
	return accessservice.NewTeamServiceWithLogger(repo, auditService, statePurger, logger)
}

type CredentialService = accessservice.CredentialService
type CredentialServiceImpl = accessservice.CredentialServiceImpl
type SSOOwnedCredentialService = accessservice.SSOOwnedCredentialService
type CreateCredentialRequest = accessservice.CreateCredentialRequest
type CredentialStore = accessservice.CredentialStore
type CredentialActivityStore = accessservice.CredentialActivityStore
type CredentialSessionInvalidator = accessservice.CredentialSessionInvalidator
type CredentialActivityWriter = accessservice.CredentialActivityWriter
type CredentialLastUsedBatchStore = accessservice.CredentialLastUsedBatchStore
type LastUsedUpdate = accessservice.LastUsedUpdate

func StandardCredentialScopes() []string { return accessservice.StandardCredentialScopes() }
func CredentialScopeValidationMessage() string {
	return accessservice.CredentialScopeValidationMessage()
}
func NormalizeCredentialScopes(scopes []string) ([]string, error) {
	return accessservice.NormalizeCredentialScopes(scopes)
}
func NormalizeCredentialRole(role string) (string, error) {
	return accessservice.NormalizeCredentialRole(role)
}

func NewCredentialService(repo CredentialStore, teams TeamService, audit AuditService, invalidator CredentialSessionInvalidator) *CredentialServiceImpl {
	return accessservice.NewCredentialService(repo, teams, audit, invalidator)
}

func NewCredentialServiceWithLogger(repo CredentialStore, teams TeamService, audit AuditService, invalidator CredentialSessionInvalidator, logger *slog.Logger) *CredentialServiceImpl {
	return accessservice.NewCredentialServiceWithLogger(repo, teams, audit, invalidator, logger)
}

func NewCredentialActivityWriter(repo CredentialActivityStore, loggers ...observability.LogProvider) *CredentialActivityWriter {
	return accessservice.NewCredentialActivityWriter(repo, loggers...)
}

func NewCredentialActivityWriterWithBatch(repo CredentialActivityStore, batchRepo CredentialLastUsedBatchStore, loggers ...observability.LogProvider) *CredentialActivityWriter {
	return accessservice.NewCredentialActivityWriterWithBatch(repo, batchRepo, loggers...)
}

type ControlIdentityConfig = accessservice.ControlIdentityConfig
type ControlIdentityService = accessservice.ControlIdentityService
type ControlLoginStart = accessservice.ControlLoginStart
type ControlLoginResult = accessservice.ControlLoginResult
type ControlIdentityStore = accessservice.ControlIdentityStore

func NewControlIdentityService(repo ControlIdentityStore, ssoRepo SSOStore, cfg ControlIdentityConfig) *ControlIdentityService {
	return accessservice.NewControlIdentityService(repo, ssoRepo, cfg)
}

type DirectoryIdentityConfig = accessservice.DirectoryIdentityConfig
type DirectoryIdentityService = accessservice.DirectoryIdentityService
type DirectoryIdentityStore = accessservice.DirectoryIdentityStore

func NewDirectoryIdentityService(repo DirectoryIdentityStore, cfg DirectoryIdentityConfig) *DirectoryIdentityService {
	return accessservice.NewDirectoryIdentityService(repo, cfg)
}

type OAuthTokenInvalidError = accessservice.OAuthTokenInvalidError
type OAuthTokenExpiredError = accessservice.OAuthTokenExpiredError
type OAuthProviderUnavailableError = accessservice.OAuthProviderUnavailableError
type OAuthProtectedResourceValidatorOptions = accessservice.OAuthProtectedResourceValidatorOptions
type OAuthProtectedResourceValidator = accessservice.OAuthProtectedResourceValidator

func NewOAuthProtectedResourceValidator(profiles []domain.OAuthProtectedResourceProfile, options OAuthProtectedResourceValidatorOptions) (*OAuthProtectedResourceValidator, error) {
	return accessservice.NewOAuthProtectedResourceValidator(profiles, options)
}

type RateLimitServiceInterface = accessservice.RateLimitServiceInterface
type RateLimitStore = accessservice.RateLimitStore
type RateLimitService = accessservice.RateLimitService

func NewRateLimitService(store RateLimitStore) *RateLimitService {
	return accessservice.NewRateLimitService(store)
}

type CleanupServiceInterface = accessservice.CleanupServiceInterface
type CleanupStore = accessservice.CleanupStore
type CleanupService = accessservice.CleanupService

func NewCleanupService(store CleanupStore) *CleanupService {
	return accessservice.NewCleanupService(store)
}

type SSOGroupResolver = accessservice.SSOGroupResolver
type SSORuntimeConfigProvider = accessservice.SSORuntimeConfigProvider
type SSORuntimeConfig = accessservice.SSORuntimeConfig
type SSOConfig = accessservice.SSOConfig
type SSOService = accessservice.SSOService
type SSOLoginStart = accessservice.SSOLoginStart
type SSOLoginResult = accessservice.SSOLoginResult
type SSOSessionInfo = accessservice.SSOSessionInfo
type SSOStore = accessservice.SSOStore
type SSOSetupErrorCode = accessservice.SSOSetupErrorCode
type SSOSetupError = accessservice.SSOSetupError
type HTTPSSOGroupResolver = accessservice.HTTPSSOGroupResolver
type OAuthProtectedResourceMetadata = accessservice.OAuthProtectedResourceMetadata

func NewSSOService(repo SSOStore, cfg SSOConfig) *SSOService {
	return accessservice.NewSSOService(repo, cfg)
}

func NewSSOSetupError(code SSOSetupErrorCode, err error) error {
	return accessservice.NewSSOSetupError(code, err)
}

func SSOSetupErrorMessage(err error) (string, bool) {
	return accessservice.SSOSetupErrorMessage(err)
}

func HashSSOToken(raw string) string { return accessservice.HashSSOToken(raw) }
func IsJWTBearer(raw string) bool    { return accessservice.IsJWTBearer(raw) }

type UserPortalSessionResult = accessservice.UserPortalSessionResult
type UserPortalSessionManager = accessservice.UserPortalSessionManager
type UserPortalSessionService = accessservice.UserPortalSessionService
type PortalSessionStore = accessservice.PortalSessionStore
type ActiveCredentialRepository = accessservice.ActiveCredentialRepository

func NewUserPortalSessionService(repo PortalSessionStore, credentials ActiveCredentialRepository, now func() time.Time) *UserPortalSessionService {
	return accessservice.NewUserPortalSessionService(repo, credentials, now)
}

const (
	CredentialScopeRead                = accessservice.CredentialScopeRead
	CredentialScopeWrite               = accessservice.CredentialScopeWrite
	CredentialScopeFeedbackRead        = accessservice.CredentialScopeFeedbackRead
	CredentialRoleManager              = accessservice.CredentialRoleManager
	CredentialRoleMember               = accessservice.CredentialRoleMember
	CredentialBindingSharedOnly        = accessservice.CredentialBindingSharedOnly
	CredentialBindingProfilePrivate    = accessservice.CredentialBindingProfilePrivate
	CredentialBindingCredentialPrivate = accessservice.CredentialBindingCredentialPrivate
	DefaultDirectoryOAuthTokenTTL      = accessservice.DefaultDirectoryOAuthTokenTTL
	DefaultDirectoryMaxAutoTeams       = accessservice.DefaultDirectoryMaxAutoTeams
	DefaultSSOEntitlementCacheTTL      = accessservice.DefaultSSOEntitlementCacheTTL
	DefaultSSOSessionTTL               = accessservice.DefaultSSOSessionTTL
	DefaultSSOStateTTL                 = accessservice.DefaultSSOStateTTL
	DefaultSSOHTTPTimeout              = accessservice.DefaultSSOHTTPTimeout
	SSOSessionCookieName               = accessservice.SSOSessionCookieName
	SSOCSRFCookieName                  = accessservice.SSOCSRFCookieName
	SSOCSRFHeaderName                  = accessservice.SSOCSRFHeaderName
	ControlSessionCookieName           = accessservice.ControlSessionCookieName
	ControlCSRFCookieName              = accessservice.ControlCSRFCookieName
	ControlCSRFHeaderName              = accessservice.ControlCSRFHeaderName
	UserPortalSessionCookieName        = accessservice.UserPortalSessionCookieName
	UserPortalCSRFCookieName           = accessservice.UserPortalCSRFCookieName
	SSOSetupGroupsMissing              = accessservice.SSOSetupGroupsMissing
	SSOSetupGroupLookupFailed          = accessservice.SSOSetupGroupLookupFailed
	SSOSetupMappingMissing             = accessservice.SSOSetupMappingMissing
	SSOSetupEntitlementEmpty           = accessservice.SSOSetupEntitlementEmpty
)

var (
	ErrControlAccessDenied        = accessservice.ErrControlAccessDenied
	ErrControlSessionInvalid      = accessservice.ErrControlSessionInvalid
	ErrControlCSRFInvalid         = accessservice.ErrControlCSRFInvalid
	ErrControlSSOUnavailable      = accessservice.ErrControlSSOUnavailable
	ErrDirectoryConnectorDisabled = accessservice.ErrDirectoryConnectorDisabled
	ErrDirectoryCredentialInvalid = accessservice.ErrDirectoryCredentialInvalid
	ErrDirectoryResourceNotFound  = accessservice.ErrDirectoryResourceNotFound
	ErrDirectoryResourceConflict  = accessservice.ErrDirectoryResourceConflict
	ErrDirectoryInvalidValue      = accessservice.ErrDirectoryInvalidValue
	ErrDirectoryPreviewStale      = accessservice.ErrDirectoryPreviewStale
	ErrOAuthTokenInvalid          = accessservice.ErrOAuthTokenInvalid
	ErrOAuthTokenExpired          = accessservice.ErrOAuthTokenExpired
	ErrOAuthProviderUnavailable   = accessservice.ErrOAuthProviderUnavailable
	ErrOAuthAccessDenied          = accessservice.ErrOAuthAccessDenied
	ErrOAuthTeamRequired          = accessservice.ErrOAuthTeamRequired
	ErrSSOAccessDenied            = accessservice.ErrSSOAccessDenied
	ErrSSOSessionInvalid          = accessservice.ErrSSOSessionInvalid
	ErrSSOCSRFInvalid             = accessservice.ErrSSOCSRFInvalid
	ErrSSOProviderDisabled        = accessservice.ErrSSOProviderDisabled
	ErrSSOEntitlementRefreshStale = accessservice.ErrSSOEntitlementRefreshStale
	ErrSSOGroupRefreshUnavailable = accessservice.ErrSSOGroupRefreshUnavailable
	ErrUserPortalSessionInvalid   = accessservice.ErrUserPortalSessionInvalid
	ErrUserPortalCSRFInvalid      = accessservice.ErrUserPortalCSRFInvalid
)
