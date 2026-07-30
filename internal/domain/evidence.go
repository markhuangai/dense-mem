package domain

// Authority represents the trust tier of a fragment or evidence source.
type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityPrimary       Authority = "primary"
	AuthoritySecondary     Authority = "secondary"
	AuthorityInferred      Authority = "inferred"
	AuthorityUnknown       Authority = "unknown"
)

// IsValid reports whether a is a recognised Authority value.
func (a Authority) IsValid() bool {
	switch a {
	case AuthorityAuthoritative, AuthorityPrimary, AuthoritySecondary, AuthorityInferred, AuthorityUnknown:
		return true
	}
	return false
}
