//go:build integration

package postgres

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/markhuangai/dense-mem/internal/domain"
	accessservice "github.com/markhuangai/dense-mem/internal/service/access"
)

func TestDirectoryPageOwnership(t *testing.T) {
	f := newDirectoryPageOwnershipFixture(t)
	ctx := context.Background()
	var results strings.Builder
	for _, kind := range []string{"users", "groups"} {
		t.Run(kind, func(t *testing.T) {
			nameField, name, externalID, id := "userName", "USER-000@EXAMPLE.TEST", "External-000", f.users[0].ID
			nameEnd := 1
			if kind == "groups" {
				nameField, name, externalID, id = "displayName", "GROUP-000", "GroupExternal-000", f.groups[0].ID
				nameEnd = 2
			}
			cases := []struct {
				name              string
				request           domain.DirectoryPageRequest
				start, end, total int
			}{
				{"metadata", domain.DirectoryPageRequest{}, 0, 0, 101},
				{"one", domain.DirectoryPageRequest{Limit: 1}, 0, 1, 101},
				{"maximum", domain.DirectoryPageRequest{Limit: 100}, 0, 100, 101},
				{"offset", domain.DirectoryPageRequest{Offset: 1, Limit: 1}, 1, 2, 101},
				{"last", domain.DirectoryPageRequest{Offset: 100, Limit: 1}, 100, 101, 101},
				{"exhausted", domain.DirectoryPageRequest{Offset: 101, Limit: 1}, 0, 0, 101},
				{"name", domain.DirectoryPageRequest{FilterField: " " + nameField + " ", FilterValue: " " + name + " ", Limit: 100}, 0, nameEnd, nameEnd},
				{"external", domain.DirectoryPageRequest{FilterField: " externalId ", FilterValue: " " + externalID + " ", Limit: 1}, 0, 1, 1},
				{"external_case_sensitive", domain.DirectoryPageRequest{FilterField: "externalId", FilterValue: strings.ToLower(externalID), Limit: 1}, 0, 0, 0},
				{"bound_value", domain.DirectoryPageRequest{FilterField: nameField, FilterValue: "' OR 1=1 --", Limit: 100}, 0, 0, 0},
				{"nil_uuid", domain.DirectoryPageRequest{FilterField: "id", FilterValue: uuid.Nil.String(), Limit: 1}, 0, 0, 0},
				{"foreign_id", domain.DirectoryPageRequest{FilterField: "id", FilterValue: directoryPageOwnershipID(11000).String(), Limit: 1}, 0, 0, 0},
			}
			if kind == "groups" {
				cases[len(cases)-1].request.FilterValue = directoryPageOwnershipID(12000).String()
			}
			for i, spelling := range []string{id.String(), strings.ToUpper(id.String()), "{" + id.String() + "}", "urn:uuid:" + id.String(), strings.ReplaceAll(id.String(), "-", "")} {
				cases = append(cases, struct {
					name              string
					request           domain.DirectoryPageRequest
					start, end, total int
				}{fmt.Sprintf("uuid_%d", i), domain.DirectoryPageRequest{FilterField: "id", FilterValue: spelling, Limit: 1}, 0, 1, 1})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					for _, direct := range []bool{false, true} {
						var signature string
						if kind == "users" {
							list := f.service.ListUsersPage
							if direct {
								list = f.repo.ListDirectoryUsersPage
							}
							users, total, err := list(ctx, f.connectorIDs[0], tc.request)
							require.NoError(t, err)
							require.Equal(t, tc.total, total)
							require.Len(t, users, tc.end-tc.start)
							for i, user := range users {
								require.Equal(t, f.users[tc.start+i], user)
							}
							signature = directoryPageOwnershipSignature(users, nil, total)
						} else {
							list := f.service.ListGroupsPage
							if direct {
								list = f.repo.ListDirectoryGroupsPage
							}
							groups, total, err := list(ctx, f.connectorIDs[0], tc.request)
							require.NoError(t, err)
							require.Equal(t, tc.total, total)
							require.Len(t, groups, tc.end-tc.start)
							for i, group := range groups {
								require.Equal(t, f.groups[tc.start+i], group)
							}
							signature = directoryPageOwnershipSignature(nil, groups, total)
						}
						fmt.Fprintf(&results, "%s/%s/%t:%s\n", kind, tc.name, direct, signature)
					}
				})
			}
			for i, request := range []domain.DirectoryPageRequest{
				{Limit: 101}, {Limit: -1}, {Offset: -1},
				{FilterField: "email", Limit: 1}, {FilterField: "id OR 1=1", Limit: 1},
				{FilterField: "id", FilterValue: "not-a-uuid", Limit: 1},
			} {
				if kind == "users" {
					_, _, err := f.service.ListUsersPage(ctx, f.connectorIDs[0], request)
					require.ErrorIs(t, err, accessservice.ErrDirectoryInvalidValue)
					_, _, err = f.repo.ListDirectoryUsersPage(ctx, f.connectorIDs[0], request)
					require.ErrorIs(t, err, ErrDirectoryInvalidValue)
				} else {
					_, _, err := f.service.ListGroupsPage(ctx, f.connectorIDs[0], request)
					require.ErrorIs(t, err, accessservice.ErrDirectoryInvalidValue)
					_, _, err = f.repo.ListDirectoryGroupsPage(ctx, f.connectorIDs[0], request)
					require.ErrorIs(t, err, ErrDirectoryInvalidValue)
				}
				fmt.Fprintf(&results, "%s/invalid_%d:service_invalid_value|contract_invalid_value\n", kind, i)
			}
		})
	}
	users, total, err := f.service.ListUsersPage(ctx, f.connectorIDs[1], domain.DirectoryPageRequest{Limit: 100})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, users, 1)
	require.Equal(t, directoryPageOwnershipID(11000), users[0].ID)
	groups, total, err := f.service.ListGroupsPage(ctx, f.connectorIDs[1], domain.DirectoryPageRequest{Limit: 100})
	require.NoError(t, err)
	require.Equal(t, 1, total)
	require.Len(t, groups, 1)
	require.Equal(t, directoryPageOwnershipID(12000), groups[0].ID)
	fmt.Fprintln(&results, directoryPageOwnershipSignature(users, groups, total))
	t.Logf("fixed_result_signature_sha256=%x", sha256.Sum256([]byte(results.String())))
}
