package contract

import (
	"github.com/google/uuid"
	"github.com/markhuangai/dense-mem/internal/domain"
)

func SeedDefinitionID(teamID string, kind Kind, key string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("dense-mem:ontology:"+teamID+":"+string(kind)+":"+key)).String()
}

func SeedRecords(teamID string, predicates []SourceSnapshot) ([]Record, error) {
	if len(predicates) > 20 {
		return nil, ErrInvalid
	}
	result := make([]Record, 0, len(domain.EntityKinds())+len(predicates))
	for _, kind := range domain.EntityKinds() {
		result = append(result, Record{ID: SeedDefinitionID(teamID, EntityClass, kind), Kind: EntityClass,
			Definition: &Definition{Key: kind, Label: kind, Description: "Existing coarse entity kind", BaseEntityKind: kind}})
	}
	for _, snapshot := range predicates {
		if snapshot.TeamID != teamID || snapshot.Kind != PredicateSource || !snapshot.Eligible {
			return nil, ErrSourceStale
		}
		fingerprint, err := SourceFingerprint(snapshot)
		if err != nil {
			return nil, err
		}
		result = append(result, Record{ID: SeedDefinitionID(teamID, PredicateConcept, snapshot.ID), Kind: PredicateConcept,
			Definition: &Definition{Key: snapshot.ID, Label: snapshot.ID, Description: "Existing registered predicate"},
			Sources:    []SourceDependency{{SourceHandle: snapshot.SourceHandle, Fingerprint: fingerprint}}})
	}
	return result, nil
}
