package domain

// SourceType represents the origin type of evidence.
type SourceType string

const (
	SourceTypeConversation SourceType = "conversation"
	SourceTypeDocument     SourceType = "document"
	SourceTypeObservation  SourceType = "observation"
	SourceTypeManual       SourceType = "manual"
)

// ValidSourceTypes returns all valid SourceType values.
func ValidSourceTypes() []SourceType {
	return []SourceType{
		SourceTypeConversation,
		SourceTypeDocument,
		SourceTypeObservation,
		SourceTypeManual,
	}
}

// IsValid reports whether a SourceType is recognized.
func (s SourceType) IsValid() bool {
	switch s {
	case SourceTypeConversation, SourceTypeDocument, SourceTypeObservation, SourceTypeManual:
		return true
	default:
		return false
	}
}
