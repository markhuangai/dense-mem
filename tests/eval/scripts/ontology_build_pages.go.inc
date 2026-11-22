// This study-only function is inserted into the scoped discovery transaction on the hosted runner.
func studyDiscoveryPageLimit(tx *gorm.DB, fence scope, kind int, turn ontology.MaintenanceTurn, limit int) (int, error) {
	const shortPage = 25
	if os.Getenv("ONTOLOGY_STUDY_ACTIVE_ARM") == "pages" && limit == ontology.MaintenancePageSize &&
		kind >= len(maintenanceSourceKinds) && turn.RetryRunID == "" {
		busy, err := maintenanceBusySources(tx, fence)
		if err != nil {
			return 0, err
		}
		keys := make([]string, 0, len(busy))
		for key := range busy {
			keys = append(keys, key)
		}
		var plentiful bool
		err = tx.Raw(`SELECT EXISTS(SELECT 1 FROM ontology_maintenance_sources
			WHERE team_id=?::uuid AND shared_space_id=?::uuid AND space_generation=?
			AND eligible AND status='pending' AND NOT (source_kind || ':' || source_id=ANY(?::text[]))
			LIMIT 1 OFFSET ?)`, fence.TeamID, fence.SpaceID, fence.Generation, pq.Array(keys), 2*shortPage-1).Row().Scan(&plentiful)
		if err != nil {
			return 0, err
		}
		if plentiful {
			limit = shortPage
		}
	}
	if observer, ok := tx.Statement.Context.Value(maintenanceDiscoveryObservationKey{}).(maintenanceDiscoveryObserver); ok && observer != nil {
		observer(maintenanceDiscoveryEvent{Phase: "study_page_limit", Scope: fence, Sources: limit})
	}
	return limit, nil
}
