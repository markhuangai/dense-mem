package contract

import "errors"

// ErrRelationshipNotFound is the adapter-to-application sentinel for a
// relationship that is outside the authenticated team's visible scope.
var ErrRelationshipNotFound = errors.New("trace relationship not found")

// ErrRelationshipIDInvalid identifies a malformed trace relationship ID.
var ErrRelationshipIDInvalid = errors.New("trace relationship ID invalid")
