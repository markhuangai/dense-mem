package service

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	cryptoutil "github.com/markhuangai/dense-mem/internal/crypto"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/repository"
)

const DefaultDirectoryOAuthTokenTTL = time.Hour

const directoryReconcileMaxAttempts = 3

var (
	ErrDirectoryConnectorDisabled = errors.New("directory connector is disabled")
	ErrDirectoryCredentialInvalid = errors.New("directory credential is invalid")
	ErrDirectoryResourceNotFound  = errors.New("directory resource not found")
	ErrDirectoryResourceConflict  = errors.New("directory resource conflict")
	ErrDirectoryInvalidValue      = errors.New("directory resource is invalid")
)

type DirectoryIdentityConfig struct {
	OAuthTokenTTL      time.Duration
	Now                func() time.Time
	CredentialVerifier cryptoutil.CredentialVerifier
}

type DirectoryIdentityService struct {
	repo          repository.DirectoryIdentityRepository
	oauthTokenTTL time.Duration
	now           func() time.Time
	verifier      cryptoutil.CredentialVerifier
}

func NewDirectoryIdentityService(repo repository.DirectoryIdentityRepository, cfg DirectoryIdentityConfig) *DirectoryIdentityService {
	ttl := cfg.OAuthTokenTTL
	if ttl <= 0 {
		ttl = DefaultDirectoryOAuthTokenTTL
	}
	now := cfg.Now
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	verifier := cfg.CredentialVerifier
	if verifier == nil {
		verifier = cryptoutil.NewArgon2Verifier(0)
	}
	return &DirectoryIdentityService{repo: repo, oauthTokenTTL: ttl, now: now, verifier: verifier}
}

func (s *DirectoryIdentityService) CreateConnector(ctx context.Context, connector domain.DirectoryConnector) (*domain.DirectoryConnector, *domain.DirectoryCredential, error) {
	if s == nil || s.repo == nil {
		return nil, nil, fmt.Errorf("directory identity service is unavailable")
	}
	connector.Status = domain.DirectoryConnectorDisabled
	connector.CredentialVersion = 1
	if err := normalizeDirectoryConnector(&connector); err != nil {
		return nil, nil, err
	}
	credential, err := newDirectoryCredential(connector.ID, connector.CredentialVersion)
	if err != nil {
		return nil, nil, err
	}
	connector.BearerTokenHash, err = cryptoutil.HashKey(credential.BearerToken)
	if err != nil {
		return nil, nil, fmt.Errorf("hash directory bearer token: %w", err)
	}
	connector.OAuthClientID = credential.OAuthClientID
	connector.OAuthClientSecretHash, err = cryptoutil.HashKey(credential.OAuthClientSecret)
	if err != nil {
		return nil, nil, fmt.Errorf("hash directory oauth client secret: %w", err)
	}
	if err := s.repo.CreateDirectoryConnector(ctx, &connector); err != nil {
		return nil, nil, err
	}
	credential.ConnectorID = connector.ID
	return &connector, credential, nil
}

func (s *DirectoryIdentityService) GetConnector(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryConnector, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	connector, err := s.repo.GetDirectoryConnector(ctx, connectorID)
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if connector == nil {
		return nil, ErrDirectoryResourceNotFound
	}
	return connector, nil
}

func (s *DirectoryIdentityService) GetConnectorForProvider(ctx context.Context, providerID uuid.UUID) (*domain.DirectoryConnector, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	if providerID == uuid.Nil {
		return nil, fmt.Errorf("sso provider ID is required")
	}
	connector, err := s.repo.GetDirectoryConnectorByProviderID(ctx, providerID)
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if connector == nil {
		return nil, ErrDirectoryResourceNotFound
	}
	return connector, nil
}

func (s *DirectoryIdentityService) ListConnectors(ctx context.Context) ([]*domain.DirectoryConnector, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	return s.repo.ListDirectoryConnectors(ctx)
}

func (s *DirectoryIdentityService) UpdateConnector(ctx context.Context, connector domain.DirectoryConnector) (*domain.DirectoryConnector, error) {
	current, err := s.GetConnector(ctx, connector.ID)
	if err != nil {
		return nil, err
	}
	if current.Status == domain.DirectoryConnectorActive && directoryConnectorPolicyChanged(*current, connector) {
		return nil, fmt.Errorf("disable the directory connector before changing its group policy")
	}
	connector.ProviderID = current.ProviderID
	connector.Status = current.Status
	connector.CredentialVersion = current.CredentialVersion
	connector.BearerTokenHash = current.BearerTokenHash
	connector.OAuthClientID = current.OAuthClientID
	connector.OAuthClientSecretHash = current.OAuthClientSecretHash
	if err := normalizeDirectoryConnector(&connector); err != nil {
		return nil, err
	}
	if err := s.repo.UpdateDirectoryConnector(ctx, &connector); err != nil {
		return nil, err
	}
	return s.GetConnector(ctx, connector.ID)
}

func (s *DirectoryIdentityService) RotateCredentials(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryCredential, error) {
	connector, err := s.GetConnector(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	credential, err := newDirectoryCredential(connector.ID, connector.CredentialVersion+1)
	if err != nil {
		return nil, err
	}
	bearerHash, err := cryptoutil.HashKey(credential.BearerToken)
	if err != nil {
		return nil, fmt.Errorf("hash directory bearer token: %w", err)
	}
	oauthSecretHash, err := cryptoutil.HashKey(credential.OAuthClientSecret)
	if err != nil {
		return nil, fmt.Errorf("hash directory oauth client secret: %w", err)
	}
	if err := s.repo.SetDirectoryCredentials(ctx, connectorID, credential.CredentialVersion, bearerHash, credential.OAuthClientID, oauthSecretHash); err != nil {
		return nil, err
	}
	return credential, nil
}

func (s *DirectoryIdentityService) Preview(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryPreview, error) {
	snapshot, err := s.connectorSnapshot(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	plan, preview, err := buildDirectoryReconcilePlan(*snapshot)
	if err != nil {
		return nil, err
	}
	_ = plan
	return &preview, nil
}

func (s *DirectoryIdentityService) SetConnectorStatus(ctx context.Context, connectorID uuid.UUID, status domain.DirectoryConnectorStatus, previewVersion string) (*domain.DirectoryConnector, error) {
	snapshot, err := s.connectorSnapshot(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	plan, preview, err := buildDirectoryReconcilePlan(*snapshot)
	if err != nil {
		return nil, err
	}
	if err := validateDirectoryConnectorTransition(snapshot.Connector, status, previewVersion, preview); err != nil {
		return nil, err
	}
	if status == domain.DirectoryConnectorActive && snapshot.Connector.Status != domain.DirectoryConnectorActive {
		if err := s.repo.ActivateDirectoryConnector(ctx, plan); err != nil {
			if errors.Is(err, repository.ErrDirectoryReconcileStale) {
				return nil, ErrDirectoryPreviewStale
			}
			return nil, err
		}
	} else if status != snapshot.Connector.Status {
		if err := s.repo.SetDirectoryConnectorStatus(ctx, connectorID, status, nil); err != nil {
			return nil, err
		}
	}
	return s.GetConnector(ctx, connectorID)
}

func (s *DirectoryIdentityService) AuthenticateSCIM(ctx context.Context, connectorID uuid.UUID, rawToken string) (bool, error) {
	connector, err := s.GetConnector(ctx, connectorID)
	if err != nil {
		return false, err
	}
	if connector.Status == domain.DirectoryConnectorDisabled {
		return false, ErrDirectoryConnectorDisabled
	}
	if strings.TrimSpace(rawToken) == "" {
		return false, ErrDirectoryCredentialInvalid
	}
	if connector.BearerTokenHash != "" {
		valid, verifyErr := s.verifier.Verify(ctx, rawToken, connector.BearerTokenHash)
		if verifyErr != nil {
			return false, verifyErr
		}
		if valid {
			return true, nil
		}
	}
	valid, err := s.repo.GetDirectoryOAuthToken(ctx, connectorID, HashSSOToken(rawToken))
	if err != nil {
		return false, err
	}
	if !valid {
		return false, ErrDirectoryCredentialInvalid
	}
	return true, nil
}

func (s *DirectoryIdentityService) IssueOAuthToken(ctx context.Context, clientID, clientSecret string) (string, time.Time, error) {
	if s == nil || s.repo == nil {
		return "", time.Time{}, fmt.Errorf("directory identity service is unavailable")
	}
	connector, err := s.repo.GetDirectoryConnectorByOAuthClientID(ctx, strings.TrimSpace(clientID))
	if err != nil {
		return "", time.Time{}, err
	}
	if connector == nil || connector.Status == domain.DirectoryConnectorDisabled {
		return "", time.Time{}, ErrDirectoryCredentialInvalid
	}
	valid, verifyErr := s.verifier.Verify(ctx, clientSecret, connector.OAuthClientSecretHash)
	if verifyErr != nil {
		return "", time.Time{}, verifyErr
	}
	if !valid {
		return "", time.Time{}, ErrDirectoryCredentialInvalid
	}
	raw, err := directorySecret("dm_scim_oauth_")
	if err != nil {
		return "", time.Time{}, err
	}
	expiresAt := s.now().Add(s.oauthTokenTTL)
	if err := s.repo.DeleteExpiredDirectoryOAuthTokens(ctx, s.now()); err != nil {
		return "", time.Time{}, err
	}
	if err := s.repo.CreateDirectoryOAuthToken(ctx, connector.ID, HashSSOToken(raw), expiresAt); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiresAt, nil
}

func (s *DirectoryIdentityService) CreateUser(ctx context.Context, connectorID uuid.UUID, user domain.DirectoryUser) (*domain.DirectoryUser, error) {
	return s.writeUser(ctx, connectorID, user, true)
}

func (s *DirectoryIdentityService) UpsertUser(ctx context.Context, connectorID uuid.UUID, user domain.DirectoryUser) (*domain.DirectoryUser, error) {
	return s.writeUser(ctx, connectorID, user, false)
}

func (s *DirectoryIdentityService) writeUser(ctx context.Context, connectorID uuid.UUID, user domain.DirectoryUser, createOnly bool) (*domain.DirectoryUser, error) {
	connector, err := s.connectorForProvisioning(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	user.ConnectorID = connector.ID
	normalizeDirectoryUser(&user)
	var stored *domain.DirectoryUser
	if createOnly {
		stored, err = s.repo.CreateDirectoryUser(ctx, user)
	} else {
		stored, err = s.repo.UpsertDirectoryUser(ctx, user)
	}
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if connector.Status == domain.DirectoryConnectorActive {
		if err := s.reconcile(ctx, connectorID); err != nil {
			return nil, err
		}
	}
	return stored, nil
}

func (s *DirectoryIdentityService) GetUser(ctx context.Context, connectorID, userID uuid.UUID) (*domain.DirectoryUser, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	user, err := s.repo.GetDirectoryUser(ctx, connectorID, userID)
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if user == nil {
		return nil, ErrDirectoryResourceNotFound
	}
	return user, nil
}

func (s *DirectoryIdentityService) ListUsers(ctx context.Context, connectorID uuid.UUID) ([]*domain.DirectoryUser, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	return s.repo.ListDirectoryUsers(ctx, connectorID)
}

func (s *DirectoryIdentityService) ListUsersPage(ctx context.Context, connectorID uuid.UUID, request domain.DirectoryPageRequest) ([]*domain.DirectoryUser, int, error) {
	if s == nil || s.repo == nil {
		return nil, 0, fmt.Errorf("directory identity service is unavailable")
	}
	users, total, err := s.repo.ListDirectoryUsersPage(ctx, connectorID, request)
	if err != nil {
		return nil, 0, directoryIdentityServiceError(err)
	}
	return users, total, nil
}

func (s *DirectoryIdentityService) DeactivateUser(ctx context.Context, connectorID, userID uuid.UUID) error {
	user, err := s.GetUser(ctx, connectorID, userID)
	if err != nil {
		return err
	}
	user.Active = false
	_, err = s.UpsertUser(ctx, connectorID, *user)
	return err
}

func (s *DirectoryIdentityService) UpsertGroup(ctx context.Context, connectorID uuid.UUID, group domain.DirectoryGroup) (*domain.DirectoryGroup, error) {
	connector, err := s.connectorForProvisioning(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	group.ConnectorID = connector.ID
	normalizeDirectoryGroup(&group)
	stored, err := s.repo.UpsertDirectoryGroup(ctx, group)
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if connector.Status == domain.DirectoryConnectorActive {
		if err := s.reconcile(ctx, connectorID); err != nil {
			return nil, err
		}
	}
	return stored, nil
}

func (s *DirectoryIdentityService) CreateGroupWithMembers(ctx context.Context, connectorID uuid.UUID, group domain.DirectoryGroup, memberIDs []uuid.UUID) (*domain.DirectoryGroup, error) {
	return s.writeGroupWithMembers(ctx, connectorID, group, memberIDs, true)
}

func (s *DirectoryIdentityService) UpsertGroupWithMembers(ctx context.Context, connectorID uuid.UUID, group domain.DirectoryGroup, memberIDs []uuid.UUID) (*domain.DirectoryGroup, error) {
	return s.writeGroupWithMembers(ctx, connectorID, group, memberIDs, false)
}

func (s *DirectoryIdentityService) writeGroupWithMembers(ctx context.Context, connectorID uuid.UUID, group domain.DirectoryGroup, memberIDs []uuid.UUID, createOnly bool) (*domain.DirectoryGroup, error) {
	connector, err := s.connectorForProvisioning(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	group.ConnectorID = connector.ID
	normalizeDirectoryGroup(&group)
	var stored *domain.DirectoryGroup
	if createOnly {
		stored, err = s.repo.CreateDirectoryGroupWithMembers(ctx, group, memberIDs)
	} else {
		stored, err = s.repo.UpsertDirectoryGroupWithMembers(ctx, group, memberIDs)
	}
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if connector.Status == domain.DirectoryConnectorActive {
		if err := s.reconcile(ctx, connectorID); err != nil {
			return nil, err
		}
	}
	return stored, nil
}

func (s *DirectoryIdentityService) GetGroup(ctx context.Context, connectorID, groupID uuid.UUID) (*domain.DirectoryGroup, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	group, err := s.repo.GetDirectoryGroup(ctx, connectorID, groupID)
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if group == nil {
		return nil, ErrDirectoryResourceNotFound
	}
	return group, nil
}

func (s *DirectoryIdentityService) ListGroups(ctx context.Context, connectorID uuid.UUID) ([]*domain.DirectoryGroup, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	return s.repo.ListDirectoryGroups(ctx, connectorID)
}

func (s *DirectoryIdentityService) ListGroupsPage(ctx context.Context, connectorID uuid.UUID, request domain.DirectoryPageRequest) ([]*domain.DirectoryGroup, int, error) {
	if s == nil || s.repo == nil {
		return nil, 0, fmt.Errorf("directory identity service is unavailable")
	}
	groups, total, err := s.repo.ListDirectoryGroupsPage(ctx, connectorID, request)
	if err != nil {
		return nil, 0, directoryIdentityServiceError(err)
	}
	return groups, total, nil
}

func (s *DirectoryIdentityService) ReplaceGroupMembers(ctx context.Context, connectorID, groupID uuid.UUID, memberIDs []uuid.UUID) error {
	connector, err := s.connectorForProvisioning(ctx, connectorID)
	if err != nil {
		return err
	}
	if err := s.repo.ReplaceDirectoryGroupMembers(ctx, connector.ID, groupID, memberIDs); err != nil {
		return directoryIdentityServiceError(err)
	}
	if connector.Status == domain.DirectoryConnectorActive {
		return s.reconcile(ctx, connectorID)
	}
	return nil
}

func (s *DirectoryIdentityService) DeactivateGroup(ctx context.Context, connectorID, groupID uuid.UUID) error {
	group, err := s.GetGroup(ctx, connectorID, groupID)
	if err != nil {
		return err
	}
	group.Active = false
	_, err = s.UpsertGroup(ctx, connectorID, *group)
	return err
}

func (s *DirectoryIdentityService) AdoptTeam(ctx context.Context, connectorID, groupID, teamID uuid.UUID) error {
	connector, err := s.connectorForProvisioning(ctx, connectorID)
	if err != nil {
		return err
	}
	if err := s.repo.AdoptDirectoryTeam(ctx, connector.ID, groupID, teamID); err != nil {
		return err
	}
	if connector.Status == domain.DirectoryConnectorActive {
		return s.reconcile(ctx, connectorID)
	}
	return nil
}

func (s *DirectoryIdentityService) connectorForProvisioning(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryConnector, error) {
	connector, err := s.GetConnector(ctx, connectorID)
	if err != nil {
		return nil, err
	}
	if connector.Status == domain.DirectoryConnectorDisabled {
		return nil, ErrDirectoryConnectorDisabled
	}
	return connector, nil
}

func (s *DirectoryIdentityService) connectorSnapshot(ctx context.Context, connectorID uuid.UUID) (*domain.DirectoryConnectorSnapshot, error) {
	if s == nil || s.repo == nil {
		return nil, fmt.Errorf("directory identity service is unavailable")
	}
	snapshot, err := s.repo.DirectoryConnectorSnapshot(ctx, connectorID)
	if err != nil {
		return nil, directoryIdentityServiceError(err)
	}
	if snapshot == nil {
		return nil, ErrDirectoryResourceNotFound
	}
	connector := snapshot.Connector
	if err := normalizeDirectoryConnector(&connector); err != nil {
		return nil, err
	}
	snapshot.Connector = connector
	return snapshot, nil
}

func directoryIdentityServiceError(err error) error {
	switch {
	case errors.Is(err, repository.ErrDirectoryResourceConflict):
		return fmt.Errorf("%w: %v", ErrDirectoryResourceConflict, err)
	case errors.Is(err, repository.ErrDirectoryInvalidValue):
		return fmt.Errorf("%w: %v", ErrDirectoryInvalidValue, err)
	}
	return err
}

func (s *DirectoryIdentityService) reconcile(ctx context.Context, connectorID uuid.UUID) error {
	for attempt := 0; attempt < directoryReconcileMaxAttempts; attempt++ {
		snapshot, err := s.connectorSnapshot(ctx, connectorID)
		if err != nil {
			return err
		}
		if snapshot.Connector.Status != domain.DirectoryConnectorActive {
			return nil
		}
		plan, _, err := buildDirectoryReconcilePlan(*snapshot)
		if err != nil {
			return err
		}
		err = s.repo.ApplyDirectoryReconcilePlan(ctx, plan)
		if !errors.Is(err, repository.ErrDirectoryReconcileStale) {
			return err
		}
	}
	return repository.ErrDirectoryReconcileStale
}

func directoryConnectorPolicyChanged(current, updated domain.DirectoryConnector) bool {
	current.GroupPattern = strings.TrimSpace(current.GroupPattern)
	updated.GroupPattern = strings.TrimSpace(updated.GroupPattern)
	if current.GroupPattern != updated.GroupPattern || current.MaxAutoTeams != updated.MaxAutoTeams {
		return true
	}
	currentJSON, _ := json.Marshal(current.RoleEntitlements)
	updatedJSON, _ := json.Marshal(updated.RoleEntitlements)
	return string(currentJSON) != string(updatedJSON)
}

func newDirectoryCredential(connectorID uuid.UUID, version int) (*domain.DirectoryCredential, error) {
	bearer, err := directorySecret("dm_scim_bearer_")
	if err != nil {
		return nil, err
	}
	clientSecret, err := directorySecret("dm_scim_client_")
	if err != nil {
		return nil, err
	}
	return &domain.DirectoryCredential{
		ConnectorID:       connectorID,
		CredentialVersion: version,
		BearerToken:       bearer,
		OAuthClientID:     "dm_scim_" + uuid.NewString(),
		OAuthClientSecret: clientSecret,
	}, nil
}

func directorySecret(prefix string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate directory credential: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

func normalizeDirectoryUser(user *domain.DirectoryUser) {
	user.ExternalID = strings.TrimSpace(user.ExternalID)
	user.UserName = strings.TrimSpace(user.UserName)
	user.Email = strings.TrimSpace(user.Email)
	user.DisplayName = strings.TrimSpace(user.DisplayName)
	if user.DisplayName == "" {
		user.DisplayName = user.UserName
	}
}

func normalizeDirectoryGroup(group *domain.DirectoryGroup) {
	group.ExternalID = strings.TrimSpace(group.ExternalID)
	group.DisplayName = strings.TrimSpace(group.DisplayName)
}

func buildDirectoryReconcilePlan(snapshot domain.DirectoryConnectorSnapshot) (domain.DirectoryReconcilePlan, domain.DirectoryPreview, error) {
	connector := snapshot.Connector
	if err := normalizeDirectoryConnector(&connector); err != nil {
		return domain.DirectoryReconcilePlan{}, domain.DirectoryPreview{}, err
	}
	re, err := directoryGroupPatternRegexp(connector.GroupPattern)
	if err != nil {
		return domain.DirectoryReconcilePlan{}, domain.DirectoryPreview{}, err
	}
	teamIndex := re.SubexpIndex("team")
	roleIndex := re.SubexpIndex("role")
	if teamIndex < 1 || roleIndex < 1 {
		return domain.DirectoryReconcilePlan{}, domain.DirectoryPreview{}, fmt.Errorf("directory group pattern captures are unavailable")
	}
	plan := domain.DirectoryReconcilePlan{
		ConnectorID:      connector.ID,
		ProviderID:       connector.ProviderID,
		ReconcileVersion: connector.ReconcileVersion,
	}
	bindings := make(map[uuid.UUID]domain.DirectoryGroupBinding, len(snapshot.Bindings))
	for _, binding := range snapshot.Bindings {
		bindings[binding.GroupID] = binding
	}
	manualMappings := make(map[string][]domain.SSOGroupMapping)
	for _, mapping := range snapshot.ManualMappings {
		manualMappings[mapping.GroupID] = append(manualMappings[mapping.GroupID], mapping)
	}
	teamsByName := make(map[string]domain.DirectoryTeam)
	teamsByID := make(map[uuid.UUID]domain.DirectoryTeam)
	for _, team := range snapshot.Teams {
		teamsByID[team.ID] = team
		teamsByName[strings.ToLower(team.Name)] = team
	}
	for _, user := range snapshot.Users {
		if user.IdentityID != uuid.Nil {
			plan.DirectoryIdentityIDs = append(plan.DirectoryIdentityIDs, user.IdentityID)
		}
	}
	existingCreatedTeams := make(map[uuid.UUID]struct{})
	for _, binding := range snapshot.Bindings {
		if binding.Origin == domain.DirectoryBindingCreated {
			existingCreatedTeams[binding.TeamID] = struct{}{}
		}
	}
	existingCreated := len(existingCreatedTeams)
	newCreated := 0
	plannedCreatedTeams := make(map[string]domain.DirectoryTeam)
	grants := make(map[string]domain.DirectoryProfileGrant)
	for _, group := range snapshot.Groups {
		groupExternalID := directoryExternalGroupID(group)
		if !group.Active {
			if binding, exists := bindings[group.ID]; exists {
				plan.DisableDirectoryGroupIDs = append(plan.DisableDirectoryGroupIDs, groupExternalID)
				if binding.Origin == domain.DirectoryBindingCreated {
					if team, found := teamsByID[binding.TeamID]; found && team.DirectoryManaged {
						plan.ArchiveDirectoryTeamIDs = append(plan.ArchiveDirectoryTeamIDs, binding.TeamID)
					}
				}
			}
			continue
		}
		if mappings := manualMappings[groupExternalID]; len(mappings) > 0 {
			plan.DisableDirectoryGroupIDs = append(plan.DisableDirectoryGroupIDs, groupExternalID)
			for _, mapping := range mappings {
				addDirectoryGroupGrants(grants, group, mapping.TeamID, groupExternalID, domain.DirectoryRoleEntitlement{Role: mapping.Role, Scopes: mapping.Scopes})
			}
			continue
		}
		if binding, exists := bindings[group.ID]; exists {
			action := domain.DirectoryBindingAction{
				GroupID:          group.ID,
				GroupExternalID:  groupExternalID,
				GroupDisplayName: group.DisplayName,
				TeamID:           binding.TeamID,
				Origin:           binding.Origin,
				Entitlement:      domain.DirectoryRoleEntitlement{Role: binding.Role, Scopes: append([]string(nil), binding.Scopes...)},
			}
			if team, found := teamsByID[binding.TeamID]; found {
				action.TeamName = team.Name
				if team.Status == "archived" && binding.Origin == domain.DirectoryBindingCreated && team.DirectoryManaged {
					plan.RestoreDirectoryTeamIDs = append(plan.RestoreDirectoryTeamIDs, binding.TeamID)
				}
			}
			plan.Bindings = append(plan.Bindings, action)
			addDirectoryGroupGrants(grants, group, action.TeamID, groupExternalID, action.Entitlement)
			continue
		}
		match := re.FindStringSubmatch(group.DisplayName)
		if len(match) <= roleIndex || len(match) <= teamIndex {
			plan.Issues = append(plan.Issues, directoryIssue(connector.ID, group.ID, domain.DirectoryIssueInvalidGroup, "group does not match the configured automation rule"))
			continue
		}
		teamName := normalizeDirectoryTeamName(match[teamIndex])
		entitlement, found := connector.RoleEntitlements[match[roleIndex]]
		if teamName == "" || !found {
			plan.Issues = append(plan.Issues, directoryIssue(connector.ID, group.ID, domain.DirectoryIssueInvalidGroup, "group captures do not produce a configured team entitlement"))
			continue
		}
		action := domain.DirectoryBindingAction{
			GroupID:          group.ID,
			GroupExternalID:  groupExternalID,
			GroupDisplayName: group.DisplayName,
			TeamName:         teamName,
			Entitlement:      entitlement,
		}
		teamKey := strings.ToLower(teamName)
		if existing, found := plannedCreatedTeams[teamKey]; found {
			action.TeamID = existing.ID
			action.Origin = domain.DirectoryBindingCreated
		} else if existing, found := teamsByName[teamKey]; found {
			if existing.Status != "active" {
				plan.Issues = append(plan.Issues, directoryIssue(connector.ID, group.ID, domain.DirectoryIssueCollision, "an archived team already has the captured name"))
				continue
			}
			if existing.DirectoryManaged {
				plan.Issues = append(plan.Issues, directoryIssue(connector.ID, group.ID, domain.DirectoryIssueCollision, "an unrelated directory-managed team already has the captured name"))
				continue
			}
			action.TeamID = existing.ID
			action.Origin = domain.DirectoryBindingExactName
		} else {
			if existingCreated+newCreated >= connector.MaxAutoTeams {
				plan.Issues = append(plan.Issues, directoryIssue(connector.ID, group.ID, domain.DirectoryIssueCapacity, "the connector auto-team cap would be exceeded"))
				continue
			}
			action.TeamID = uuid.NewSHA1(connector.ID, []byte("team:"+teamKey))
			action.CreateTeam = true
			action.Origin = domain.DirectoryBindingCreated
			plannedCreatedTeams[teamKey] = domain.DirectoryTeam{ID: action.TeamID, Name: teamName, Status: "active", DirectoryManaged: true}
			newCreated++
		}
		plan.Bindings = append(plan.Bindings, action)
		addDirectoryGroupGrants(grants, group, action.TeamID, groupExternalID, action.Entitlement)
	}
	plan.DirectoryIdentityIDs = uniqueDirectoryIdentityIDs(plan.DirectoryIdentityIDs)
	plan.DisableDirectoryGroupIDs = uniqueDirectoryStrings(plan.DisableDirectoryGroupIDs)
	activeCreatedTeams := make(map[uuid.UUID]struct{})
	for _, action := range plan.Bindings {
		if action.Origin == domain.DirectoryBindingCreated {
			activeCreatedTeams[action.TeamID] = struct{}{}
		}
	}
	archiveDirectoryTeamIDs := make([]uuid.UUID, 0, len(plan.ArchiveDirectoryTeamIDs))
	for _, teamID := range uniqueDirectoryIdentityIDs(plan.ArchiveDirectoryTeamIDs) {
		if _, active := activeCreatedTeams[teamID]; !active {
			archiveDirectoryTeamIDs = append(archiveDirectoryTeamIDs, teamID)
		}
	}
	plan.ArchiveDirectoryTeamIDs = archiveDirectoryTeamIDs
	plan.RestoreDirectoryTeamIDs = uniqueDirectoryIdentityIDs(plan.RestoreDirectoryTeamIDs)
	plan.ProfileGrants = directoryProfileGrants(grants)
	preview := directoryPreviewFromPlan(plan)
	preview.Version = directoryPlanVersion(plan)
	return plan, preview, nil
}

func directoryExternalGroupID(group domain.DirectoryGroup) string {
	if strings.TrimSpace(group.ExternalID) != "" {
		return group.ExternalID
	}
	return group.ID.String()
}

func normalizeDirectoryTeamName(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if len(value) > 100 {
		return ""
	}
	return value
}

func directoryIssue(connectorID, groupID uuid.UUID, kind domain.DirectoryIssueKind, detail string) domain.DirectoryIssue {
	key := "group:" + groupID.String() + ":" + string(kind)
	return domain.DirectoryIssue{ConnectorID: connectorID, GroupID: &groupID, Key: key, Kind: kind, Detail: detail, Active: true}
}

func addDirectoryGroupGrants(grants map[string]domain.DirectoryProfileGrant, group domain.DirectoryGroup, teamID uuid.UUID, groupExternalID string, entitlement domain.DirectoryRoleEntitlement) {
	for _, user := range group.Members {
		if !user.Active || user.IdentityID == uuid.Nil {
			continue
		}
		subject := user.ExternalID
		if subject == "" {
			subject = "scim:" + user.ID.String()
		}
		candidate := domain.DirectoryProfileGrant{
			IdentityID:      user.IdentityID,
			TeamID:          teamID,
			Subject:         subject,
			Email:           user.Email,
			DisplayName:     user.DisplayName,
			GroupExternalID: groupExternalID,
			Entitlement:     entitlement,
		}
		key := candidate.IdentityID.String() + ":" + candidate.TeamID.String()
		if existing, found := grants[key]; found {
			grants[key] = strongestDirectoryGrant(existing, candidate)
		} else {
			grants[key] = candidate
		}
	}
}

func strongestDirectoryGrant(existing, candidate domain.DirectoryProfileGrant) domain.DirectoryProfileGrant {
	merged := append(append([]string(nil), existing.Entitlement.Scopes...), candidate.Entitlement.Scopes...)
	scopes, err := NormalizeAPIKeyScopes(merged)
	winner := existing
	if directoryEntitlementRank(candidate.Entitlement) > directoryEntitlementRank(existing.Entitlement) {
		winner = candidate
	}
	if err == nil {
		winner.Entitlement.Scopes = scopes
	}
	return winner
}

func directoryEntitlementRank(entitlement domain.DirectoryRoleEntitlement) int {
	rank := len(entitlement.Scopes)
	if entitlement.Role == APIKeyRoleManager {
		rank += 100
	}
	return rank
}

func directoryProfileGrants(values map[string]domain.DirectoryProfileGrant) []domain.DirectoryProfileGrant {
	items := make([]domain.DirectoryProfileGrant, 0, len(values))
	for _, value := range values {
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].TeamID == items[j].TeamID {
			return items[i].IdentityID.String() < items[j].IdentityID.String()
		}
		return items[i].TeamID.String() < items[j].TeamID.String()
	})
	return items
}

func directoryPreviewFromPlan(plan domain.DirectoryReconcilePlan) domain.DirectoryPreview {
	candidates := make([]domain.DirectoryPreviewCandidate, 0, len(plan.Bindings))
	for _, action := range plan.Bindings {
		candidates = append(candidates, domain.DirectoryPreviewCandidate{
			GroupID:       action.GroupID,
			ExternalID:    action.GroupExternalID,
			DisplayName:   action.GroupDisplayName,
			TeamID:        action.TeamID,
			TeamName:      action.TeamName,
			Entitlement:   action.Entitlement,
			BindingOrigin: action.Origin,
		})
	}
	issues := append([]domain.DirectoryIssue(nil), plan.Issues...)
	return domain.DirectoryPreview{Candidates: candidates, Issues: issues}
}

func directoryPlanVersion(plan domain.DirectoryReconcilePlan) string {
	type versionBinding struct {
		GroupID    string
		ExternalID string
		Display    string
		TeamID     string
		TeamName   string
		Create     bool
		Origin     string
		Role       string
		Scopes     []string
	}
	type versionIssue struct {
		Key    string
		Kind   string
		Detail string
	}
	type versionGrant struct {
		IdentityID string
		TeamID     string
		Subject    string
		Email      string
		GroupID    string
		Role       string
		Scopes     []string
	}
	payload := struct {
		ConnectorID      string
		ProviderID       string
		ReconcileVersion int64
		Bindings         []versionBinding
		Disable          []string
		Archive          []string
		Restore          []string
		Grants           []versionGrant
		Identities       []string
		Issues           []versionIssue
	}{
		ConnectorID:      plan.ConnectorID.String(),
		ProviderID:       plan.ProviderID.String(),
		ReconcileVersion: plan.ReconcileVersion,
		Disable:          append([]string(nil), plan.DisableDirectoryGroupIDs...),
	}
	for _, action := range plan.Bindings {
		payload.Bindings = append(payload.Bindings, versionBinding{
			GroupID: action.GroupID.String(), ExternalID: action.GroupExternalID, Display: action.GroupDisplayName, TeamID: action.TeamID.String(), TeamName: action.TeamName,
			Create: action.CreateTeam, Origin: string(action.Origin), Role: action.Entitlement.Role, Scopes: append([]string(nil), action.Entitlement.Scopes...),
		})
	}
	for _, teamID := range plan.ArchiveDirectoryTeamIDs {
		payload.Archive = append(payload.Archive, teamID.String())
	}
	for _, teamID := range plan.RestoreDirectoryTeamIDs {
		payload.Restore = append(payload.Restore, teamID.String())
	}
	for _, grant := range plan.ProfileGrants {
		payload.Grants = append(payload.Grants, versionGrant{
			IdentityID: grant.IdentityID.String(), TeamID: grant.TeamID.String(), Subject: grant.Subject,
			Email: grant.Email, GroupID: grant.GroupExternalID, Role: grant.Entitlement.Role,
			Scopes: append([]string(nil), grant.Entitlement.Scopes...),
		})
	}
	for _, identityID := range plan.DirectoryIdentityIDs {
		payload.Identities = append(payload.Identities, identityID.String())
	}
	for _, issue := range plan.Issues {
		payload.Issues = append(payload.Issues, versionIssue{Key: issue.Key, Kind: string(issue.Kind), Detail: issue.Detail})
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func uniqueDirectoryIdentityIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func uniqueDirectoryStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
