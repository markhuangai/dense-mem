package access

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
)

const DefaultDirectoryMaxAutoTeams = 100

var ErrDirectoryPreviewStale = errors.New("directory preview is stale")

func normalizeDirectoryConnector(connector *domain.DirectoryConnector) error {
	if connector == nil {
		return fmt.Errorf("%w: directory connector is required", ErrDirectoryInvalidValue)
	}
	if connector.ProviderID == uuid.Nil {
		return fmt.Errorf("%w: sso provider ID is required", ErrDirectoryInvalidValue)
	}
	if connector.Status == "" {
		connector.Status = domain.DirectoryConnectorDisabled
	}
	switch connector.Status {
	case domain.DirectoryConnectorDisabled, domain.DirectoryConnectorObserve, domain.DirectoryConnectorActive:
	default:
		return fmt.Errorf("%w: directory connector status is invalid", ErrDirectoryInvalidValue)
	}
	connector.GroupPattern = strings.TrimSpace(connector.GroupPattern)
	if connector.GroupPattern == "" {
		return fmt.Errorf("%w: directory group_pattern is required", ErrDirectoryInvalidValue)
	}
	if !strings.HasPrefix(connector.GroupPattern, "^") || !strings.HasSuffix(connector.GroupPattern, "$") {
		return fmt.Errorf("%w: directory group_pattern must be anchored with ^ and $", ErrDirectoryInvalidValue)
	}
	re, err := directoryGroupPatternRegexp(connector.GroupPattern)
	if err != nil {
		return fmt.Errorf("%w: directory group_pattern is invalid: %w", ErrDirectoryInvalidValue, err)
	}
	if namedSubexpressionCount(re, "team") != 1 || namedSubexpressionCount(re, "role") != 1 {
		return fmt.Errorf("%w: directory group_pattern must contain exactly one named team capture and one named role capture", ErrDirectoryInvalidValue)
	}
	if connector.MaxAutoTeams == 0 {
		connector.MaxAutoTeams = DefaultDirectoryMaxAutoTeams
	}
	if connector.MaxAutoTeams < 1 || connector.MaxAutoTeams > 1000 {
		return fmt.Errorf("%w: directory max_auto_teams must be between 1 and 1000", ErrDirectoryInvalidValue)
	}
	entitlements, err := normalizeDirectoryRoleEntitlements(connector.RoleEntitlements)
	if err != nil {
		return err
	}
	connector.RoleEntitlements = entitlements
	return nil
}

func directoryGroupPatternRegexp(pattern string) (*regexp.Regexp, error) {
	return regexp.Compile("^(?:" + pattern + ")$")
}

func namedSubexpressionCount(re *regexp.Regexp, name string) int {
	count := 0
	for _, candidate := range re.SubexpNames() {
		if candidate == name {
			count++
		}
	}
	return count
}

func normalizeDirectoryRoleEntitlements(values map[string]domain.DirectoryRoleEntitlement) (map[string]domain.DirectoryRoleEntitlement, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("%w: directory role_entitlements is required", ErrDirectoryInvalidValue)
	}
	normalized := make(map[string]domain.DirectoryRoleEntitlement, len(values))
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		capture := strings.TrimSpace(key)
		if capture == "" {
			return nil, fmt.Errorf("%w: directory role entitlement capture is required", ErrDirectoryInvalidValue)
		}
		if _, exists := normalized[capture]; exists {
			return nil, fmt.Errorf("%w: directory role entitlement capture %q is duplicated", ErrDirectoryInvalidValue, capture)
		}
		entitlement := values[key]
		role, err := NormalizeCredentialRole(entitlement.Role)
		if err != nil {
			return nil, err
		}
		if role == "" {
			return nil, fmt.Errorf("%w: directory role entitlement %q must set a membership role", ErrDirectoryInvalidValue, capture)
		}
		if len(entitlement.Scopes) == 0 {
			return nil, fmt.Errorf("%w: directory role entitlement %q must grant at least one scope", ErrDirectoryInvalidValue, capture)
		}
		scopes, err := NormalizeCredentialScopes(entitlement.Scopes)
		if err != nil {
			return nil, err
		}
		if role == CredentialRoleManager {
			scopes = managerCredentialScopes(scopes)
		}
		if strings.EqualFold(capture, "manager") && role != CredentialRoleManager {
			return nil, fmt.Errorf("%w: directory role entitlement %q must map to manager", ErrDirectoryInvalidValue, capture)
		}
		if isReadOnlyDirectoryRole(capture) && containsString(scopes, CredentialScopeWrite) {
			return nil, fmt.Errorf("%w: directory role entitlement %q must not grant write", ErrDirectoryInvalidValue, capture)
		}
		normalized[capture] = domain.DirectoryRoleEntitlement{Role: role, Scopes: scopes}
	}
	return normalized, nil
}

func isReadOnlyDirectoryRole(value string) bool {
	normalized := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(value), "-", ""))
	return normalized == "readonly" || normalized == "read"
}

func validateDirectoryConnectorTransition(current domain.DirectoryConnector, desired domain.DirectoryConnectorStatus, previewVersion string, preview domain.DirectoryPreview) error {
	if desired == "" {
		return fmt.Errorf("%w: directory connector status is required", ErrDirectoryInvalidValue)
	}
	if current.Status == desired {
		return nil
	}
	switch current.Status {
	case domain.DirectoryConnectorDisabled:
		if desired == domain.DirectoryConnectorObserve {
			return nil
		}
	case domain.DirectoryConnectorObserve:
		if desired == domain.DirectoryConnectorDisabled {
			return nil
		}
		if desired == domain.DirectoryConnectorActive {
			if strings.TrimSpace(previewVersion) == "" {
				return fmt.Errorf("%w: preview version is required before activation", ErrDirectoryResourceConflict)
			}
			if previewVersion != preview.Version {
				return ErrDirectoryPreviewStale
			}
			return nil
		}
	case domain.DirectoryConnectorActive:
		if desired == domain.DirectoryConnectorDisabled {
			return nil
		}
	}
	return fmt.Errorf("%w: connector must transition through observe before active", ErrDirectoryInvalidValue)
}
