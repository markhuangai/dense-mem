package contract

const TopicProjectionPageSize = 100

type TopicCatalogCursor struct {
	TeamID  string
	TopicID string
}

type TopicCatalogEntry struct {
	TeamID     string
	SpaceID    string
	Generation int64
	TopicID    string
	Version    int64
}

type TopicCatalogPage struct {
	Topics []TopicCatalogEntry
	Next   TopicCatalogCursor
	More   bool
}

type TopicMembershipCursor struct {
	AssignmentID   string
	RelationshipID string
}

type TopicMembershipPage struct {
	Topic           RecordView
	RelationshipIDs []string
	Records         []Record
	Sources         []SourceHandle
	Next            TopicMembershipCursor
	More            bool
}
