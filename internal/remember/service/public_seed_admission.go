package service

import (
	"errors"
	"io"
	"regexp"

	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/jsonstrict"
	"github.com/markhuangai/dense-mem/internal/requestctx"
)

const PublicSeedAdmissionPath = "/run/dense-mem-seed-admission.json"

type PublicSeedAdmissionManifest struct {
	TeamID       uuid.UUID         `json:"team_id"`
	CredentialID uuid.UUID         `json:"credential_id"`
	OwnerID      uuid.UUID         `json:"owner_id"`
	SpaceID      uuid.UUID         `json:"space_id"`
	Generation   int64             `json:"generation"`
	Requests     map[string]string `json:"requests"`
}

type PublicSeedAdmission struct {
	manifest PublicSeedAdmissionManifest
}

var publicSeedRequestHash = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func ReadPublicSeedAdmission(reader io.Reader) (*PublicSeedAdmission, error) {
	var manifest PublicSeedAdmissionManifest
	if err := jsonstrict.Decode(reader, &manifest, 8<<20); err != nil {
		return nil, errors.New("invalid public seed admission manifest")
	}
	if manifest.TeamID == uuid.Nil || manifest.CredentialID == uuid.Nil || manifest.OwnerID == uuid.Nil || manifest.SpaceID == uuid.Nil || manifest.Generation < 1 || len(manifest.Requests) == 0 {
		return nil, errors.New("incomplete public seed admission manifest")
	}
	for key, hash := range manifest.Requests {
		if key == "" || len(key) > 128 || !publicSeedRequestHash.MatchString(hash) {
			return nil, errors.New("invalid public seed admission request")
		}
	}
	return &PublicSeedAdmission{manifest: manifest}, nil
}

func (p *PublicSeedAdmission) allows(actor requestctx.Actor, space domain.MemorySpaceAccess, key, requestHash string) bool {
	m := p.manifest
	return actor.CredentialID != nil && *actor.CredentialID == m.CredentialID && actor.TeamID == m.TeamID && actor.OwnerID == m.OwnerID &&
		space.Kind == domain.MemorySpaceTeamShared && space.ID == m.SpaceID && space.Generation == m.Generation && m.Requests[key] == requestHash
}
