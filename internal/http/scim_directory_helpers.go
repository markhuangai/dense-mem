package http

import (
	"errors"
	nethttp "net/http"
	"strconv"
	"strings"
	"time"

	"github.com/elimity-com/scim"
	scimerrors "github.com/elimity-com/scim/errors"
	"github.com/elimity-com/scim/optional"
	"github.com/google/uuid"

	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/service"
)

func directorySCIMUserResource(user domain.DirectoryUser) scim.Resource {
	attributes := scim.ResourceAttributes{
		"userName":    user.UserName,
		"displayName": user.DisplayName,
		"active":      user.Active,
	}
	if user.Email != "" {
		attributes["emails"] = []interface{}{map[string]interface{}{"value": user.Email, "type": "work", "primary": true}}
	}
	return directorySCIMResource(user.ID, user.ExternalID, attributes, user.CreatedAt, user.UpdatedAt)
}

func directorySCIMGroupResource(group domain.DirectoryGroup) scim.Resource {
	attributes := scim.ResourceAttributes{"displayName": group.DisplayName}
	members := make([]interface{}, 0, len(group.Members))
	for _, member := range group.Members {
		members = append(members, map[string]interface{}{"value": member.ID.String(), "display": member.DisplayName, "type": "User"})
	}
	if len(members) > 0 {
		attributes["members"] = members
	}
	return directorySCIMResource(group.ID, group.ExternalID, attributes, group.CreatedAt, group.UpdatedAt)
}

func directorySCIMResource(id uuid.UUID, externalID string, attributes scim.ResourceAttributes, createdAt, updatedAt time.Time) scim.Resource {
	resource := scim.Resource{ID: id.String(), Attributes: attributes, Meta: scim.Meta{Created: &createdAt, LastModified: &updatedAt}}
	if externalID != "" {
		resource.ExternalID = optional.NewString(externalID)
	}
	return resource
}

type directorySCIMFilter struct {
	field string
	value string
}

func directoryParseSCIMFilter(request *nethttp.Request, allowed ...string) (directorySCIMFilter, error) {
	raw := strings.TrimSpace(request.URL.Query().Get("filter"))
	if raw == "" {
		return directorySCIMFilter{}, nil
	}
	parts := strings.SplitN(raw, " eq ", 2)
	if len(parts) != 2 {
		return directorySCIMFilter{}, scimerrors.ScimErrorInvalidFilter
	}
	field := strings.TrimSpace(parts[0])
	allowedField := false
	for _, candidate := range allowed {
		if field == candidate {
			allowedField = true
			break
		}
	}
	if !allowedField {
		return directorySCIMFilter{}, scimerrors.ScimErrorInvalidFilter
	}
	value, err := strconv.Unquote(strings.TrimSpace(parts[1]))
	if err != nil {
		return directorySCIMFilter{}, scimerrors.ScimErrorInvalidFilter
	}
	return directorySCIMFilter{field: field, value: value}, nil
}

func directorySCIMUserMatches(user domain.DirectoryUser, filter directorySCIMFilter) bool {
	switch filter.field {
	case "":
		return true
	case "userName":
		return strings.EqualFold(user.UserName, filter.value)
	case "externalId":
		return user.ExternalID == filter.value
	case "id":
		return user.ID.String() == filter.value
	default:
		return false
	}
}

func directorySCIMPageRequest(filter directorySCIMFilter, params scim.ListRequestParams) domain.DirectoryPageRequest {
	offset := params.StartIndex - 1
	if offset < 0 {
		offset = 0
	}
	limit := params.Count
	if limit < 0 {
		limit = 0
	}
	if limit > directorySCIMMaxResults {
		limit = directorySCIMMaxResults
	}
	return domain.DirectoryPageRequest{FilterField: filter.field, FilterValue: filter.value, Offset: offset, Limit: limit}
}

func directorySCIMGroupMatches(group domain.DirectoryGroup, filter directorySCIMFilter) bool {
	switch filter.field {
	case "":
		return true
	case "displayName":
		return group.DisplayName == filter.value
	case "externalId":
		return group.ExternalID == filter.value
	case "id":
		return group.ID.String() == filter.value
	default:
		return false
	}
}

func directorySCIMPage(resources []scim.Resource, params scim.ListRequestParams) scim.Page {
	total := len(resources)
	start := params.StartIndex - 1
	if start < 0 {
		start = 0
	}
	if start >= total || params.Count == 0 {
		return scim.Page{TotalResults: total, Resources: []scim.Resource{}}
	}
	end := start + params.Count
	if end > total {
		end = total
	}
	return scim.Page{TotalResults: total, Resources: resources[start:end]}
}

func directorySCIMString(value interface{}) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	return strings.TrimSpace(text), true
}

func directorySCIMBool(value interface{}) (bool, bool) {
	result, ok := value.(bool)
	return result, ok
}

func directorySCIMNestedString(value interface{}, key string) (string, bool) {
	attributes, ok := value.(map[string]interface{})
	if !ok {
		return "", false
	}
	return directorySCIMString(attributes[key])
}

func directorySCIMEmail(value interface{}) (string, bool) {
	items, ok := value.([]interface{})
	if !ok {
		return "", false
	}
	if len(items) == 0 {
		return "", true
	}
	fallback := ""
	for _, item := range items {
		attributes, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		email, ok := directorySCIMString(attributes["value"])
		if !ok || email == "" {
			continue
		}
		if primary, present := directorySCIMBool(attributes["primary"]); present && primary {
			return email, true
		}
		if fallback == "" {
			fallback = email
		}
	}
	return fallback, fallback != ""
}

func directorySCIMMemberIDs(value interface{}) ([]uuid.UUID, error) {
	if value == nil {
		return []uuid.UUID{}, nil
	}
	items, ok := value.([]interface{})
	if !ok {
		return nil, scimerrors.ScimErrorInvalidValue
	}
	members := make([]uuid.UUID, 0, len(items))
	for _, item := range items {
		attributes, ok := item.(map[string]interface{})
		if !ok {
			return nil, scimerrors.ScimErrorInvalidValue
		}
		rawID, ok := directorySCIMString(attributes["value"])
		if !ok {
			return nil, scimerrors.ScimErrorInvalidValue
		}
		memberID, err := uuid.Parse(rawID)
		if err != nil {
			return nil, scimerrors.ScimErrorInvalidValue
		}
		members = append(members, memberID)
	}
	return directoryUniqueUserIDs(members), nil
}

func directorySCIMGetError(id string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, service.ErrDirectoryResourceNotFound) {
		return scimerrors.ScimErrorResourceNotFound(id)
	}
	return scimerrors.ScimErrorInternal
}

func directorySCIMListError(err error) error {
	if errors.Is(err, service.ErrDirectoryInvalidValue) {
		return scimerrors.ScimErrorInvalidFilter
	}
	return scimerrors.ScimErrorInternal
}

func directorySCIMMutationError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, service.ErrDirectoryConnectorDisabled) {
		return scimerrors.ScimError{Status: nethttp.StatusForbidden}
	}
	if errors.Is(err, service.ErrDirectoryResourceConflict) {
		return scimerrors.ScimErrorUniqueness
	}
	if errors.Is(err, service.ErrDirectoryInvalidValue) {
		return scimerrors.ScimErrorInvalidValue
	}
	return scimerrors.ScimErrorInternal
}

func directoryUserIDs(users []domain.DirectoryUser) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(users))
	for _, user := range users {
		if user.ID != uuid.Nil {
			result = append(result, user.ID)
		}
	}
	return directoryUniqueUserIDs(result)
}

func directoryUniqueUserIDs(values []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(values))
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value == uuid.Nil {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func directoryRemoveUserID(values []uuid.UUID, target uuid.UUID) []uuid.UUID {
	result := make([]uuid.UUID, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func directoryMemberIDFromPath(path string) (uuid.UUID, error) {
	value := strings.TrimSuffix(strings.TrimPrefix(path, "members[value eq "), "]")
	value, err := strconv.Unquote(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(value)
}
