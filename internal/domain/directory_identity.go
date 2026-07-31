package domain

import (
	"time"

	"github.com/google/uuid"
)

type DirectoryConnectorStatus string

const (
	DirectoryConnectorDisabled DirectoryConnectorStatus = "disabled"
	DirectoryConnectorObserve  DirectoryConnectorStatus = "observe"
	DirectoryConnectorActive   DirectoryConnectorStatus = "active"
)

type DirectoryBindingOrigin string

const (
	DirectoryBindingCreated   DirectoryBindingOrigin = "directory_created"
	DirectoryBindingExactName DirectoryBindingOrigin = "exact_name"
	DirectoryBindingAdopted   DirectoryBindingOrigin = "adopted"
)

type DirectoryIssueKind string

const (
	DirectoryIssueInvalidGroup DirectoryIssueKind = "invalid_group"
	DirectoryIssueAmbiguous    DirectoryIssueKind = "ambiguous_group"
	DirectoryIssueCollision    DirectoryIssueKind = "team_collision"
	DirectoryIssueCapacity     DirectoryIssueKind = "auto_team_capacity"
)

type DirectoryRoleEntitlement struct {
	Role   string   `json:"role"`
	Scopes []string `json:"scopes"`
}

type DirectoryConnector struct {
	ID               uuid.UUID
	ProviderID       uuid.UUID
	Status           DirectoryConnectorStatus
	GroupPattern     string
	RoleEntitlements map[string]DirectoryRoleEntitlement
	MaxAutoTeams     int

	CredentialVersion     int
	BearerTokenHash       string
	OAuthClientID         string
	OAuthClientSecretHash string
	LastActivationAt      *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

type DirectoryUser struct {
	ID          uuid.UUID
	ConnectorID uuid.UUID
	ExternalID  string
	UserName    string
	Email       string
	DisplayName string
	Active      bool
	IdentityID  uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DirectoryGroup struct {
	ID          uuid.UUID
	ConnectorID uuid.UUID
	ExternalID  string
	DisplayName string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	Members     []DirectoryUser
}

type DirectoryGroupBinding struct {
	ConnectorID uuid.UUID
	GroupID     uuid.UUID
	TeamID      uuid.UUID
	Origin      DirectoryBindingOrigin
	Scopes      []string
	Role        string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DirectoryIssue struct {
	ID          uuid.UUID
	ConnectorID uuid.UUID
	GroupID     *uuid.UUID
	Key         string
	Kind        DirectoryIssueKind
	Detail      string
	Active      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type DirectoryPreviewCandidate struct {
	GroupID       uuid.UUID                `json:"group_id"`
	ExternalID    string                   `json:"external_id"`
	DisplayName   string                   `json:"display_name"`
	TeamName      string                   `json:"team_name"`
	Entitlement   DirectoryRoleEntitlement `json:"entitlement"`
	BindingOrigin DirectoryBindingOrigin   `json:"binding_origin"`
}

type DirectoryPreview struct {
	Version    string                      `json:"version"`
	Candidates []DirectoryPreviewCandidate `json:"candidates"`
	Issues     []DirectoryIssue            `json:"issues"`
}

type DirectoryTeam struct {
	ID                   uuid.UUID
	Name                 string
	Status               string
	DirectoryManaged     bool
	DirectoryConnectorID *uuid.UUID
	DirectoryGroupID     *uuid.UUID
}

type DirectoryConnectorSnapshot struct {
	Connector      DirectoryConnector
	Groups         []DirectoryGroup
	Users          []DirectoryUser
	Bindings       []DirectoryGroupBinding
	ManualMappings []SSOGroupMapping
	Teams          []DirectoryTeam
}

type DirectoryBindingAction struct {
	GroupID          uuid.UUID
	GroupExternalID  string
	GroupDisplayName string
	TeamID           uuid.UUID
	TeamName         string
	CreateTeam       bool
	Origin           DirectoryBindingOrigin
	Entitlement      DirectoryRoleEntitlement
}

type DirectoryProfileGrant struct {
	IdentityID      uuid.UUID
	TeamID          uuid.UUID
	Subject         string
	Email           string
	DisplayName     string
	GroupExternalID string
	Entitlement     DirectoryRoleEntitlement
}

type DirectoryReconcilePlan struct {
	ConnectorID              uuid.UUID
	ProviderID               uuid.UUID
	Bindings                 []DirectoryBindingAction
	DisableDirectoryGroupIDs []string
	ArchiveDirectoryTeamIDs  []uuid.UUID
	RestoreDirectoryTeamIDs  []uuid.UUID
	ProfileGrants            []DirectoryProfileGrant
	DirectoryIdentityIDs     []uuid.UUID
	Issues                   []DirectoryIssue
}

type DirectoryCredential struct {
	ConnectorID       uuid.UUID `json:"connector_id"`
	CredentialVersion int       `json:"credential_version"`
	BearerToken       string    `json:"bearer_token"`
	OAuthClientID     string    `json:"oauth_client_id"`
	OAuthClientSecret string    `json:"oauth_client_secret"`
}
