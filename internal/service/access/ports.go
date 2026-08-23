package access

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

// AuditLogEntry is the access capability's narrow audit value. It deliberately
// contains only durable, redacted payloads and authenticated actor metadata.
type AuditLogEntry struct {
	ID            string
	ProfileID     *string
	MemorySpaceID *string
	Timestamp     time.Time
	Operation     string
	EntityType    string
	EntityID      string
	BeforePayload map[string]interface{}
	AfterPayload  map[string]interface{}
	ActorKeyID    *string
	ActorRole     string
	ClientIP      string
	CorrelationID string
	Metadata      map[string]interface{}
}

// AuditService is the consumer-owned audit port used by access policy. The
// concrete audit writer remains in the operations service and is injected at
// composition time.
type AuditService interface {
	Append(context.Context, AuditLogEntry) error
	List(context.Context, string, int, int) ([]AuditLogEntry, int, error)
	TeamCreated(context.Context, string, map[string]interface{}, *string, string, string, string) error
	TeamUpdated(context.Context, string, map[string]interface{}, map[string]interface{}, *string, string, string, string) error
	TeamDeleteBlocked(context.Context, string, map[string]interface{}, *string, string, string, string, string) error
	TeamDeleted(context.Context, string, map[string]interface{}, *string, string, string, string) error
	CredentialCreated(context.Context, *string, string, map[string]interface{}, *string, string, string, string) error
	CredentialRevoked(context.Context, *string, string, map[string]interface{}, *string, string, string, string) error
	AuthFailure(context.Context, *string, string, string, map[string]interface{}, string, string) error
	CrossTeamDenied(context.Context, string, string, string, map[string]interface{}, string, string) error
	RateLimited(context.Context, *string, string, map[string]interface{}, string, string) error
	SystemQuery(context.Context, string, map[string]interface{}, *string, string, string, string) error
	InvariantViolation(context.Context, string, string, string, map[string]interface{}, string, string) error
}

// Access coordination and storage ports are declared beside the policy that
// consumes them. Existing adapters satisfy these interfaces structurally.
type TeamStore interface {
	Create(context.Context, *domain.Team) error
	GetByID(context.Context, uuid.UUID) (*domain.Team, error)
	List(context.Context, int, int) ([]*domain.Team, error)
	Count(context.Context) (int64, error)
	Update(context.Context, *domain.Team) error
	SoftDelete(context.Context, uuid.UUID) error
	HardDelete(context.Context, uuid.UUID) error
	CountActiveKeys(context.Context, uuid.UUID) (int64, error)
	NameExists(context.Context, string) (bool, error)
}

type CredentialStore interface {
	CreateCredential(context.Context, *domain.Credential) error
	ListByTeam(context.Context, uuid.UUID, int, int) ([]*domain.Credential, error)
	CountByTeam(context.Context, uuid.UUID) (int64, error)
	GetByIDForTeam(context.Context, uuid.UUID, uuid.UUID) (*domain.Credential, error)
	ListSSOOwnedCredentials(context.Context, uuid.UUID, uuid.UUID) ([]*domain.Credential, error)
	GetSSOOwnedCredentialByID(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (*domain.Credential, error)
	GetSSOOwnedCredential(context.Context, uuid.UUID, uuid.UUID) (*domain.Credential, error)
	GetActiveByPrefix(context.Context, string) (*domain.Credential, error)
	RevokeForTeam(context.Context, uuid.UUID, uuid.UUID) (int64, error)
	DeleteForTeam(context.Context, uuid.UUID, uuid.UUID) (int64, error)
	UpdateNameForTeam(context.Context, uuid.UUID, uuid.UUID, string) (int64, error)
	UpdateRoleForTeam(context.Context, uuid.UUID, uuid.UUID, string, []string) (int64, error)
	UpdateScopesForTeam(context.Context, uuid.UUID, uuid.UUID, []string) (int64, error)
	RotateForTeam(context.Context, uuid.UUID, uuid.UUID, string, string, string, *time.Time) (int64, error)
	TouchLastUsed(context.Context, uuid.UUID) error
}

type LastUsedUpdate struct {
	ID uuid.UUID
	At time.Time
}

type CredentialLastUsedBatchStore interface {
	TouchLastUsedBatch(context.Context, []LastUsedUpdate) error
}

type CredentialActivityStore interface {
	TouchLastUsed(context.Context, uuid.UUID) error
}

type CredentialDeletionAuditStore interface {
	DeleteForTeamWithAudit(context.Context, uuid.UUID, uuid.UUID, CredentialDeletionAuditInput) (int64, error)
}

type CredentialDeletionAuditInput struct {
	ActorCredentialID *string
	ActorRole         string
	ClientIP          string
	CorrelationID     string
}

type SSOStore interface {
	ListProviders(context.Context) ([]*domain.SSOProvider, error)
	ListEnabledProviders(context.Context) ([]*domain.SSOProvider, error)
	GetProvider(context.Context, uuid.UUID) (*domain.SSOProvider, error)
	CreateProvider(context.Context, *domain.SSOProvider) error
	UpdateProvider(context.Context, *domain.SSOProvider) error
	UpdateProviderPreservingProtectedResource(context.Context, *domain.SSOProvider) error
	DeleteProvider(context.Context, uuid.UUID) error
	ListMappings(context.Context, uuid.UUID) ([]*domain.SSOGroupMapping, error)
	CreateMapping(context.Context, *domain.SSOGroupMapping) error
	UpdateMapping(context.Context, *domain.SSOGroupMapping) error
	DeleteMapping(context.Context, uuid.UUID, uuid.UUID) error
	ListMappingsForGroups(context.Context, uuid.UUID, []string) ([]*domain.SSOGroupMapping, error)
	DirectoryAuthorityActive(context.Context, uuid.UUID) (bool, error)
	DirectoryMembershipEntitled(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string) (bool, error)
	UpsertIdentity(context.Context, *domain.SSOIdentity) error
	GetIdentity(context.Context, uuid.UUID) (*domain.SSOIdentity, error)
	GetIdentityByProviderSubject(context.Context, uuid.UUID, string) (*domain.SSOIdentity, error)
	UpsertTeamMembershipForMapping(context.Context, domain.SSOIdentity, domain.SSOGroupMapping, string) (*domain.Membership, error)
	ListTeamMembershipsForIdentity(context.Context, uuid.UUID) ([]*domain.SSOTeamMembership, error)
	GetTeamMembershipByOwnerID(context.Context, uuid.UUID) (*domain.SSOTeamMembership, error)
	GetEntitlementCache(context.Context, uuid.UUID, string) (*domain.SSOEntitlementCache, error)
	SetEntitlementCache(context.Context, domain.SSOEntitlementCache) error
	CreateOAuthState(context.Context, domain.SSOOAuthState) error
	ConsumeOAuthState(context.Context, string) (*domain.SSOOAuthState, error)
	DeleteExpiredOAuthStates(context.Context, time.Time) error
	CreateSession(context.Context, domain.SSOSession) error
	GetSession(context.Context, string) (*domain.SSOSession, error)
	UpdateSessionTeam(context.Context, string, uuid.UUID) error
	DeleteSession(context.Context, string) error
	DeleteExpiredSessions(context.Context, time.Time) error
}

type ControlIdentityStore interface {
	ListControlAdminGroups(context.Context, uuid.UUID) ([]*domain.ControlAdminGroup, error)
	CreateControlAdminGroup(context.Context, *domain.ControlAdminGroup) error
	UpdateControlAdminGroup(context.Context, *domain.ControlAdminGroup) error
	RetireControlAdminGroup(context.Context, uuid.UUID, uuid.UUID) error
	CreateControlOAuthState(context.Context, domain.ControlOAuthState) error
	ConsumeControlOAuthState(context.Context, string) (*domain.ControlOAuthState, error)
	DeleteExpiredControlOAuthStates(context.Context, time.Time) error
	CreateControlSession(context.Context, domain.ControlSession) error
	GetControlSession(context.Context, string) (*domain.ControlSession, error)
	DeleteControlSession(context.Context, string) error
	DeleteExpiredControlSessions(context.Context, time.Time) error
}

type DirectoryIdentityStore interface {
	CreateDirectoryConnector(context.Context, *domain.DirectoryConnector) error
	GetDirectoryConnector(context.Context, uuid.UUID) (*domain.DirectoryConnector, error)
	GetDirectoryConnectorByProviderID(context.Context, uuid.UUID) (*domain.DirectoryConnector, error)
	GetDirectoryConnectorByOAuthClientID(context.Context, string) (*domain.DirectoryConnector, error)
	ListDirectoryConnectors(context.Context) ([]*domain.DirectoryConnector, error)
	UpdateDirectoryConnector(context.Context, *domain.DirectoryConnector) error
	SetDirectoryConnectorStatus(context.Context, uuid.UUID, domain.DirectoryConnectorStatus, *time.Time) error
	SetDirectoryCredentials(context.Context, uuid.UUID, int, string, string, string) error
	CreateDirectoryOAuthToken(context.Context, uuid.UUID, string, time.Time) error
	GetDirectoryOAuthToken(context.Context, uuid.UUID, string) (bool, error)
	DeleteExpiredDirectoryOAuthTokens(context.Context, time.Time) error
	CreateDirectoryUser(context.Context, domain.DirectoryUser) (*domain.DirectoryUser, error)
	UpsertDirectoryUser(context.Context, domain.DirectoryUser) (*domain.DirectoryUser, error)
	GetDirectoryUser(context.Context, uuid.UUID, uuid.UUID) (*domain.DirectoryUser, error)
	ListDirectoryUsers(context.Context, uuid.UUID) ([]*domain.DirectoryUser, error)
	ListDirectoryUsersPage(context.Context, uuid.UUID, domain.DirectoryPageRequest) ([]*domain.DirectoryUser, int, error)
	UpsertDirectoryGroup(context.Context, domain.DirectoryGroup) (*domain.DirectoryGroup, error)
	CreateDirectoryGroupWithMembers(context.Context, domain.DirectoryGroup, []uuid.UUID) (*domain.DirectoryGroup, error)
	UpsertDirectoryGroupWithMembers(context.Context, domain.DirectoryGroup, []uuid.UUID) (*domain.DirectoryGroup, error)
	GetDirectoryGroup(context.Context, uuid.UUID, uuid.UUID) (*domain.DirectoryGroup, error)
	ListDirectoryGroups(context.Context, uuid.UUID) ([]*domain.DirectoryGroup, error)
	ListDirectoryGroupsPage(context.Context, uuid.UUID, domain.DirectoryPageRequest) ([]*domain.DirectoryGroup, int, error)
	ReplaceDirectoryGroupMembers(context.Context, uuid.UUID, uuid.UUID, []uuid.UUID) error
	DirectoryConnectorSnapshot(context.Context, uuid.UUID) (*domain.DirectoryConnectorSnapshot, error)
	ApplyDirectoryReconcilePlan(context.Context, domain.DirectoryReconcilePlan) error
	ActivateDirectoryConnector(context.Context, domain.DirectoryReconcilePlan) error
	AdoptDirectoryTeam(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error
}

type PortalSessionStore interface {
	CreateSession(context.Context, *domain.UserPortalSession) error
	GetSession(context.Context, string) (*domain.UserPortalSession, error)
	DeleteSession(context.Context, string) error
	DeleteExpiredSessions(context.Context, time.Time) error
}

type RateLimitStore interface {
	IncrWithExpire(context.Context, string, int64) (int64, error)
}

type TeamStatePurger interface {
	PurgeTeamState(context.Context, string) error
}

type CredentialSessionInvalidator interface {
	InvalidateCredentialSessions(context.Context, string, string) error
}

type CleanupStore interface {
	PurgeTeamState(context.Context, string) error
	InvalidateCredentialSessions(context.Context, string, string) error
}

type DirectoryCredentialVerifier interface {
	Verify(string, string) bool
}
