package contract

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
)

func TestDirectoryPageRequestNormalization(t *testing.T) {
	t.Parallel()
	for _, resource := range []struct {
		name      string
		nameField string
		normalize func(domain.DirectoryPageRequest) (domain.DirectoryPageRequest, error)
	}{
		{"user", "userName", NormalizeDirectoryUserPageRequest},
		{"group", "displayName", NormalizeDirectoryGroupPageRequest},
	} {
		t.Run(resource.name, func(t *testing.T) {
			for _, limit := range []int{0, 1, 100} {
				request := domain.DirectoryPageRequest{Offset: 2, Limit: limit, FilterField: " " + resource.nameField + " ", FilterValue: " Name "}
				got, err := resource.normalize(request)
				require.NoError(t, err)
				require.Equal(t, domain.DirectoryPageRequest{Offset: 2, Limit: limit, FilterField: resource.nameField, FilterValue: "Name"}, got)
				again, err := resource.normalize(got)
				require.NoError(t, err)
				require.Equal(t, got, again)
			}
			for _, request := range []domain.DirectoryPageRequest{{Offset: -1}, {Limit: -1}, {Limit: 101}} {
				got, err := resource.normalize(request)
				require.ErrorIs(t, err, ErrDirectoryInvalidValue)
				require.ErrorContains(t, err, "directory page bounds are invalid")
				require.Equal(t, domain.DirectoryPageRequest{}, got)
			}
			for _, field := range []string{"", "externalId", resource.nameField} {
				got, err := resource.normalize(domain.DirectoryPageRequest{FilterField: field, FilterValue: " ' OR 1=1 -- ", Limit: 1})
				require.NoError(t, err)
				require.Equal(t, "' OR 1=1 --", got.FilterValue)
			}
			for _, field := range []string{"email", "ID", "user_name", "id OR 1=1", "connector_id"} {
				_, err := resource.normalize(domain.DirectoryPageRequest{FilterField: field, Limit: 1})
				require.ErrorIs(t, err, ErrDirectoryInvalidValue)
				require.ErrorContains(t, err, "directory "+resource.name+" filter is invalid")
			}
			id := uuid.MustParse("12aabcde-0000-4000-8000-000000000001")
			for _, value := range []string{id.String(), strings.ToUpper(id.String()), "{" + id.String() + "}", "urn:uuid:" + id.String(), strings.ReplaceAll(id.String(), "-", ""), " " + id.String() + " ", uuid.Nil.String()} {
				got, err := resource.normalize(domain.DirectoryPageRequest{FilterField: " id ", FilterValue: value, Limit: 1})
				require.NoError(t, err)
				expected := id.String()
				if value == uuid.Nil.String() {
					expected = uuid.Nil.String()
				}
				require.Equal(t, expected, got.FilterValue)
			}
			for _, value := range []string{"", "not-a-uuid", "' OR 1=1 --"} {
				_, err := resource.normalize(domain.DirectoryPageRequest{FilterField: "id", FilterValue: value})
				require.ErrorIs(t, err, ErrDirectoryInvalidValue)
				require.ErrorContains(t, err, "directory "+resource.name+" id filter is invalid")
			}
		})
	}
	_, err := NormalizeDirectoryUserPageRequest(domain.DirectoryPageRequest{FilterField: "displayName"})
	require.ErrorIs(t, err, ErrDirectoryInvalidValue)
	_, err = NormalizeDirectoryGroupPageRequest(domain.DirectoryPageRequest{FilterField: "userName"})
	require.ErrorIs(t, err, ErrDirectoryInvalidValue)
}
