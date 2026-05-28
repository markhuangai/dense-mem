package factservice

import "errors"

// ErrPredicateNotPoliced is returned when a promote request names a predicate
// that is not present in the gate map. HTTP callers MUST map this to 422.
//
// Security invariant: unknown predicates are denied by default. This prevents
// accidental promotion of unvalidated knowledge.
var ErrPredicateNotPoliced = errors.New("predicate not policed")

// ErrClaimNotFound is returned when a promote request targets a Claim that is
// absent or belongs to a different profile. HTTP callers MUST map this to the
// same 404 used by normal claim reads so cross-profile access leaks nothing.
var ErrClaimNotFound = errors.New("claim not found for promote")

// ErrUnsupportedPolicy is returned when a gate's Policy is not implemented by
// the promotion service. HTTP callers MUST map this to 422.
var ErrUnsupportedPolicy = errors.New("unsupported policy")

// ErrClaimNotValidated is returned when a promote request targets a Claim that
// does not have Status == StatusValidated. HTTP callers MUST map this to 409.
var ErrClaimNotValidated = errors.New("claim not validated")

// ErrGateRejected is returned when one or more promotion gate thresholds
// (extract_conf, resolution_conf, modality, verdict, support count) are not
// met. HTTP callers MUST map this to 409.
var ErrGateRejected = errors.New("gate rejected")

// ErrPromotionDeferredDisputed is returned when the claim's strength is
// comparable to an existing active fact's strength and no supersession can be
// determined. The claim transitions to "disputed". HTTP callers MUST map this
// to 409.
var ErrPromotionDeferredDisputed = errors.New("promotion deferred: claim disputed")

// ErrPromotionRejected is returned when the claim is weaker than an existing
// active fact for the same (subject, predicate) triple. HTTP callers MUST map
// this to 409.
var ErrPromotionRejected = errors.New("promotion rejected: claim weaker than existing fact")

// ErrInvalidConfirmationDecision is returned when confirm_memory receives an
// unsupported decision value.
var ErrInvalidConfirmationDecision = errors.New("invalid confirmation decision")
