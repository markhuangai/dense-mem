package postgres

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/ontology/assessment"
	ontology "github.com/markhuangai/dense-mem/internal/ontology/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	"github.com/stretchr/testify/require"
)

func testOntologyOrganizationVocabularyAndSourceFences(t *testing.T) {
	t.Run("oversized dependency context records a zero-call failure", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		dependency := f.source(t, source)
		revision := int64(0)
		for offset := 0; offset < ontology.MaxDependencyRecords; offset += ontology.MaxChanges {
			var changes []ontology.Change
			for i := offset; i < offset+ontology.MaxChanges && i < ontology.MaxDependencyRecords; i++ {
				record := testTopic(fmt.Sprintf("context-%d", i))
				record.Sources = []ontology.SourceDependency{dependency}
				changes = append(changes, ontology.Change{Record: record})
			}
			result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(fmt.Sprintf("context-batch-%d", offset), revision, changes...))
			require.NoError(t, err)
			revision = result.Revision
		}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("extra-vocabulary", revision, ontology.Change{Record: testTopic("postgresql")}))
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := ontology.OrganizationInput{OperationKey: "oversized-dependencies", Sources: []ontology.SourceHandle{source}}
		result, err := service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrInvalid)
		require.Equal(t, "invalid_publication", result.FailureCode)
		require.Nil(t, result.Publication)
		require.Zero(t, calls.Load())
		require.Equal(t, 1, receiptCount(t, f))
		replay, err := service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.True(t, replay.Existing)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Zero(t, calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))

		extra := testTopic("context-overflow")
		extra.Sources = []ontology.SourceDependency{dependency}
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("source-dependent-overflow", revision+1, ontology.Change{Record: extra}))
		require.NoError(t, err)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{source})
		require.ErrorIs(t, err, ontology.ErrContextBound)
		require.Len(t, contextData.Sources, 1)
		require.Empty(t, contextData.Records)
		var historyCount int64
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_record_revisions WHERE team_id=?::uuid`, f.team).Row().Scan(&historyCount))
		require.Equal(t, int64(ontology.MaxDependencyRecords+2), historyCount)
		input.OperationKey = "source-dependent-overflow"
		result, err = service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrContextBound)
		require.Equal(t, "invalid_publication", result.FailureCode)
		require.False(t, result.Current)
		require.Nil(t, result.Publication)
		require.Empty(t, result.Attempts)
		require.Equal(t, []ontology.OrganizationOutcome{{Source: source, Status: "failed", Reason: "invalid_publication"}}, result.Outcomes)
		require.Equal(t, 2, receiptCount(t, f))
		replay, err = service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.True(t, replay.Existing)
		require.False(t, replay.Current)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Equal(t, result.Outcomes, replay.Outcomes)
		input.Sources = []ontology.SourceHandle{source, {Kind: ontology.EvidenceSource, ID: uuid.NewString(), Version: 1}}
		_, err = service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrConflict)
		require.Equal(t, 2, receiptCount(t, f))
		var unchangedHistoryCount int64
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM ontology_record_revisions WHERE team_id=?::uuid`, f.team).Row().Scan(&unchangedHistoryCount))
		require.Equal(t, historyCount, unchangedHistoryCount)
		require.Zero(t, calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("oversized pinned vocabulary records a zero-call failure", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		topic := testTopic("postgresql")
		publication, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("pinned-topic", 0, ontology.Change{Record: topic}))
		require.NoError(t, err)
		originalService, originalCalls := organizationFixtureService(t, f, nil, nil)
		originalInput := ontology.OrganizationInput{OperationKey: "before-pinned-vocabulary-overflow", Sources: []ontology.SourceHandle{source}}
		original, err := originalService.Organize(context.Background(), f.team, originalInput)
		require.NoError(t, err)
		require.True(t, original.Current)
		require.NotNil(t, original.Publication)
		require.Equal(t, int32(1), originalCalls.Load())
		publication = *original.Publication
		for offset := 0; offset < ontology.MaxDependencyRecords; offset += ontology.MaxChanges {
			var changes []ontology.Change
			for i := offset; i < offset+ontology.MaxChanges && i < ontology.MaxDependencyRecords; i++ {
				pin := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: topic.ID}}
				changes = append(changes, ontology.Change{Record: pin})
			}
			publication, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(fmt.Sprintf("pin-batch-%d", offset), publication.Revision, changes...))
			require.NoError(t, err)
		}
		unclassified := f.organizationEvidence(t, 1, "Beacon uses PostgreSQL.", nil)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{unclassified})
		require.ErrorIs(t, err, ontology.ErrContextBound)
		require.Len(t, contextData.Sources, 1)
		require.Empty(t, contextData.Records)
		organizationSnapshot := func() map[string]string {
			result := map[string]string{}
			for _, table := range []string{"ontology_catalog_heads", "ontology_record_heads", "ontology_record_revisions", "ontology_publications"} {
				var value string
				require.NoError(t, f.admin.Raw(`SELECT COALESCE(jsonb_agg(to_jsonb(row) ORDER BY to_jsonb(row)::text)::text,'[]') FROM `+table+` AS row WHERE team_id=?::uuid`, f.team).Row().Scan(&value))
				result[table] = value
			}
			return result
		}
		before, beforeOrganization := f.canonicalSnapshot(t), organizationSnapshot()
		service, calls := organizationFixtureService(t, f, nil, nil)
		completedReplay, err := service.Organize(context.Background(), f.team, originalInput)
		require.NoError(t, err)
		require.True(t, completedReplay.Existing)
		require.False(t, completedReplay.Current)
		require.Equal(t, original.AssessmentID, completedReplay.AssessmentID)
		require.Equal(t, original.Publication, completedReplay.Publication)
		require.Equal(t, original.Outcomes, completedReplay.Outcomes)
		require.Equal(t, 1, receiptCount(t, f))
		input := ontology.OrganizationInput{OperationKey: "pinned-vocabulary-overflow", Sources: []ontology.SourceHandle{unclassified}}
		result, err := service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrContextBound)
		require.Equal(t, "invalid_publication", result.FailureCode)
		require.False(t, result.Current)
		require.Nil(t, result.Publication)
		require.Empty(t, result.Attempts)
		require.Equal(t, []ontology.OrganizationOutcome{{Source: unclassified, Status: "failed", Reason: "invalid_publication"}}, result.Outcomes)
		replay, err := service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.True(t, replay.Existing)
		require.False(t, replay.Current)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Equal(t, result.Outcomes, replay.Outcomes)
		input.Sources = []ontology.SourceHandle{source, unclassified}
		_, err = service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, ontology.ErrConflict)
		require.Equal(t, 2, receiptCount(t, f))
		require.Zero(t, calls.Load())
		require.Equal(t, beforeOrganization, organizationSnapshot())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	for _, relation := range []string{"distinct", "ambiguous"} {
		t.Run("locked relative-time comparison "+relation, func(t *testing.T) {
			f := newOrganizationFixture(t)
			a := f.evidenceAt(t, 0, "Atlas releases next week.", "2026-01-05T12:00:00Z")
			b := f.evidenceAt(t, 0, "Atlas releases next week.", "2026-01-12T12:00:00Z")
			service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
				if len(request.Items) == 2 {
					for _, item := range request.Items {
						require.NotEmpty(t, item.LockedDefinitionRef)
					}
					require.Len(t, request.Pairs, 1)
					require.Empty(t, request.Pairs[0].RequiredRelation)
					response.Equivalence[0].Relation = relation
				}
			})
			for i, source := range []ontology.SourceHandle{a, b} {
				_, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: fmt.Sprintf("classify-relative-%d", i), Sources: []ontology.SourceHandle{source}})
				require.NoError(t, err)
			}
			before := f.canonicalSnapshot(t)
			priorCalls := calls.Load()
			result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "compare-relative", Sources: []ontology.SourceHandle{a, b}})
			require.NoError(t, err)
			require.Equal(t, priorCalls+1, calls.Load())
			require.Len(t, result.Attempts, 1)
			groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
			require.NoError(t, err)
			require.Empty(t, groups.Records)
			if relation == "ambiguous" {
				require.Len(t, result.AmbiguousComparisons, 1)
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
	for _, match := range []string{"key", "label", "alias"} {
		for _, pinned := range []bool{false, true} {
			name := "reuse omitted current definition"
			if pinned {
				name = "reuse omitted current pinned definition"
			}
			t.Run(name+"/"+match, func(t *testing.T) {
				f := newOrganizationFixture(t)
				oldSource := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
				const newText = "Beacon relies on a relational store."
				service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
					if request.Items[0].Text == newText {
						require.Empty(t, request.Definitions)
						require.Len(t, response.Definitions, 1)
						response.Definitions[0].Label = "Provider replacement label"
						if match != "key" {
							response.Definitions[0].Key = "relational-store"
							if match == "label" {
								response.Definitions[0].Label = " POSTGRESQL "
							} else {
								response.Definitions[0].Aliases = []string{" POSTGRESQL "}
							}
						}
						response.Definitions[0].Description = "Provider replacement description"
					}
				})
				original, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "current-definition", Sources: []ontology.SourceHandle{oldSource}})
				require.NoError(t, err)
				id := ontology.OrganizationRecordID(f.team, ontology.Topic, "postgresql")
				if pinned {
					pin := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.PinDefinition, TargetID: id}}
					_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("pin-current-definition", original.Publication.Revision, ontology.Change{Record: pin}))
					require.NoError(t, err)
				}
				definition, err := f.store.GetRecord(context.Background(), f.team, id, 0)
				require.NoError(t, err)
				require.True(t, definition.Current)
				newSource := f.organizationEvidence(t, 1, newText, nil)
				contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{newSource})
				require.NoError(t, err)
				require.Empty(t, contextData.Candidates)
				before := f.canonicalSnapshot(t)
				input := ontology.OrganizationInput{OperationKey: "reuse-current-definition", Sources: []ontology.SourceHandle{newSource}}
				result, err := service.Organize(context.Background(), f.team, input)
				require.NoError(t, err)
				require.NotNil(t, result.Publication)
				reused, err := f.store.GetRecord(context.Background(), f.team, id, 0)
				require.NoError(t, err)
				require.True(t, reused.Current)
				require.Equal(t, definition.Record, reused.Record)
				if match != "key" {
					_, err = f.store.GetRecord(context.Background(), f.team, ontology.OrganizationRecordID(f.team, ontology.Topic, "relational-store"), 0)
					require.ErrorIs(t, err, ontology.ErrNotFound)
				}
				for _, source := range []ontology.SourceHandle{oldSource, newSource} {
					assignment, err := f.store.GetRecord(context.Background(), f.team, ontology.OrganizationRecordID(f.team, ontology.AssignmentKind, ontology.SourceKey(source)), 0)
					require.NoError(t, err)
					require.True(t, assignment.Current)
					require.Equal(t, id, assignment.Assignment.DefinitionID)
				}
				input.OperationKey = "reuse-current-definition-replay"
				replay, err := service.Organize(context.Background(), f.team, input)
				require.NoError(t, err)
				require.True(t, replay.Existing)
				require.Equal(t, result.AssessmentID, replay.AssessmentID)
				require.Equal(t, int32(2), calls.Load())
				require.Equal(t, before, f.canonicalSnapshot(t))
			})
		}
	}
	t.Run("definition name lookup is kind and team scoped", func(t *testing.T) {
		f := newOrganizationFixture(t)
		topic := testTopic("postgresql")
		class := ontology.Record{ID: uuid.NewString(), Kind: ontology.EntityClass, Definition: &ontology.Definition{Key: "person", Label: "PostgreSQL", BaseEntityKind: "person"}}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("same-name-different-kinds", 0, ontology.Change{Record: topic}, ontology.Change{Record: class}))
		require.NoError(t, err)
		query := ontology.Record{ID: ontology.OrganizationRecordID(f.team, ontology.Topic, "relational-store"), Kind: ontology.Topic, Definition: &ontology.Definition{Key: "relational-store", Label: "Relational storage", Aliases: []string{" POSTGRESQL "}}}
		heads, err := f.store.ReadDefinitionHeads(context.Background(), f.team, query)
		require.NoError(t, err)
		require.Len(t, heads, 1)
		require.Equal(t, topic.ID, heads[0].ID)
		_, err = f.store.ReadDefinitionHeads(f.actor(1, "member"), f.team, query)
		require.ErrorIs(t, err, ontology.ErrUnauthorized)
		other := &domain.Team{Name: "definition-name-c-" + uuid.NewString()}
		require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), other))
		heads, err = f.store.ReadDefinitionHeads(context.Background(), other.ID.String(), query)
		require.NoError(t, err)
		require.Empty(t, heads)
	})
	for _, overridden := range []bool{false, true} {
		name := "withdrawn member does not block remaining source"
		if overridden {
			name = "persistent grouping override still requires its members"
		}
		t.Run(name, func(t *testing.T) {
			f := newOrganizationFixture(t)
			a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
			memberOwner, memberText := 1, "PostgreSQL is Atlas's data store."
			if overridden {
				memberOwner, memberText = 0, "Atlas uses PostgreSQL."
			}
			b := f.organizationEvidence(t, memberOwner, memberText, nil)
			require.NotEqual(t, a.ID, b.ID)
			service, calls := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
			original, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "original-group", Sources: []ontology.SourceHandle{a, b}})
			require.NoError(t, err)
			groups, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
			require.NoError(t, err)
			require.Len(t, groups.Records, 1)
			group := groups.Records[0].Record
			if overridden {
				override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.GroupTogether, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
				_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("persistent-grouping", original.Publication.Revision, ontology.Change{Record: override}))
				require.NoError(t, err)
			}
			_, err = f.knowledge.RetractEvidence(f.actor(memberOwner, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[memberOwner], EvidenceIDs: []string{b.ID}, Reason: "withdraw grouped member", IdempotencyKey: "withdraw-member", RequestHash: testHash("withdraw-member")})
			require.NoError(t, err)
			before := f.canonicalSnapshot(t)
			contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{a})
			require.NoError(t, err)
			found := false
			for _, view := range contextData.Records {
				if view.ID == group.ID {
					found = true
					require.False(t, view.Current)
				}
			}
			require.True(t, found)
			priorCalls := calls.Load()
			input := ontology.OrganizationInput{OperationKey: "remaining-source", Sources: []ontology.SourceHandle{a}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			if overridden {
				require.Equal(t, "ambiguous", result.Outcomes[0].Status)
				require.Equal(t, "resubmit_complete_group", result.Outcomes[0].Reason)
				require.Equal(t, priorCalls, calls.Load())
			} else {
				require.Equal(t, "organized", result.Outcomes[0].Status)
				require.NotNil(t, result.Publication)
				require.Equal(t, priorCalls+1, calls.Load())
			}
			retained, err := f.store.GetRecord(context.Background(), f.team, group.ID, 0)
			require.NoError(t, err)
			require.Equal(t, group, retained.Record)
			require.False(t, retained.Current)
			priorCalls = calls.Load()
			input.OperationKey = "remaining-source-replay"
			replay, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, result.AssessmentID, replay.AssessmentID)
			require.Equal(t, priorCalls, calls.Load())
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
	t.Run("effective manager definitions survive stale assignments", func(t *testing.T) {
		f := newOrganizationFixture(t)
		var handles []ontology.SourceHandle
		var definitions, assignments, overrides []ontology.Change
		for i := range 11 {
			source := f.organizationEvidence(t, i%2, fmt.Sprintf("Record number %d uses an isolated database.", i), nil)
			handles = append(handles, source)
			old := testTopic(fmt.Sprintf("old-category-%d", i))
			old.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			manager := testTopic(fmt.Sprintf("manager-category-%d", i))
			manager.ID = fmt.Sprintf("ffffffff-ffff-4fff-8fff-%012d", i+1)
			definitions = append(definitions, ontology.Change{Record: old}, ontology.Change{Record: manager})
			dependency := f.source(t, source)
			assignment := ontology.Record{ID: ontology.OrganizationRecordID(f.team, ontology.AssignmentKind, ontology.SourceKey(source)), Kind: ontology.AssignmentKind, Assignment: &ontology.Assignment{Source: source, DefinitionID: old.ID}, Sources: []ontology.SourceDependency{dependency}}
			assignments = append(assignments, ontology.Change{Record: assignment})
			override := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.SetClassification, Members: []ontology.SourceHandle{source}, DefinitionID: manager.ID}, Sources: []ontology.SourceDependency{dependency}}
			overrides = append(overrides, ontology.Change{Record: override})
		}
		definitionsResult, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("vocabulary-definitions", 0, definitions...))
		require.NoError(t, err)
		assignmentResult, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("old-assignments", definitionsResult.Revision, assignments...))
		require.NoError(t, err)
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("effective-overrides", assignmentResult.Revision, overrides...))
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, handles)
		require.NoError(t, err)
		require.LessOrEqual(t, len(contextData.Candidates), ontology.MaxVocabularyCandidates)
		candidates := map[string]bool{}
		for _, candidate := range contextData.Candidates {
			candidates[candidate.ID] = true
		}
		original := map[string]ontology.Record{}
		for i, assignment := range assignments {
			view, err := f.store.GetRecord(context.Background(), f.team, assignment.Record.ID, 0)
			require.NoError(t, err)
			require.False(t, view.Current)
			original[view.ID] = view.Record
			require.True(t, candidates[overrides[i].Record.Override.DefinitionID])
		}
		service, calls := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			require.Len(t, request.Items, 11)
			require.Empty(t, response.Definitions)
			for _, item := range request.Items {
				require.NotEmpty(t, item.LockedDefinitionRef)
			}
		})
		input := ontology.OrganizationInput{OperationKey: "manager-vocabulary-bound", Sources: handles}
		result, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.NotNil(t, result.Publication)
		require.True(t, result.Current)
		require.Len(t, result.Outcomes, 11)
		for _, outcome := range result.Outcomes {
			require.Equal(t, "organized", outcome.Status)
			require.Empty(t, outcome.Reason)
		}
		for i, assignment := range assignments {
			view, err := f.store.GetRecord(context.Background(), f.team, assignment.Record.ID, 0)
			require.NoError(t, err)
			require.True(t, view.Current)
			require.Equal(t, int64(2), view.Version)
			require.Equal(t, overrides[i].Record.Override.DefinitionID, view.Assignment.DefinitionID)
			history, err := f.store.GetRecord(context.Background(), f.team, view.ID, 1)
			require.NoError(t, err)
			require.Equal(t, original[view.ID], history.Record)
		}
		for _, key := range []string{input.OperationKey, "manager-vocabulary-bound-replay"} {
			input.OperationKey = key
			replay, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.True(t, replay.Existing)
			require.Equal(t, result.AssessmentID, replay.AssessmentID)
			require.Equal(t, result.Outcomes, replay.Outcomes)
		}
		require.Equal(t, int32(1), calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("bounded vocabulary reuse", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		changes := []ontology.Change{}
		for i := 0; i < 24; i++ {
			changes = append(changes, ontology.Change{Record: testTopic(fmt.Sprintf("PostgreSQL memory %d", i))})
		}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("vocabulary", 0, changes...))
		require.NoError(t, err)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []ontology.SourceHandle{source})
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, 20)
		service, _ := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			require.Len(t, request.Definitions, 20)
			require.Empty(t, response.Definitions)
		})
		_, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "reuse", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.Topic, "", 200)
		require.NoError(t, err)
		require.Len(t, page.Records, 24)
	})
	t.Run("private and old generation", func(t *testing.T) {
		f := newOrganizationFixture(t)
		private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-private", KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}, MemoryBinding: domain.CredentialBindingCredentialPrivate}
		require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
		privateCtx := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpaceCredentialPrivate, Generation: private.MemorySpaceGeneration}})
		stored, err := f.knowledge.CreateIngestForTest(privateCtx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private source"}}})
		require.NoError(t, err)
		service, calls := organizationFixtureService(t, f, nil, nil)
		privateHandle := ontology.SourceHandle{Kind: ontology.EvidenceSource, ID: stored.Evidence[0].FragmentID, Version: 1}
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "private", Sources: []ontology.SourceHandle{privateHandle}})
		require.NoError(t, err)
		require.Equal(t, "unavailable", result.Outcomes[0].Status)
		require.Zero(t, calls.Load())
		source := f.organizationEvidence(t, 0, "Shared old generation", nil)
		require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Error)
		result, err = service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "old-generation", Sources: []ontology.SourceHandle{source}})
		require.NoError(t, err)
		require.Equal(t, "unavailable", result.Outcomes[0].Status)
		require.Zero(t, calls.Load())
	})
	t.Run("override arrives during assessment", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		b := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, _ := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, func(request assessment.Request, response *assessment.Response) {
			separation := ontology.Record{ID: uuid.NewString(), Kind: ontology.OverrideKind, Override: &ontology.Override{Action: ontology.KeepSeparate, Members: []ontology.SourceHandle{a, b}}, Sources: []ontology.SourceDependency{f.source(t, a), f.source(t, b)}}
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("concurrent-separation", 0, ontology.Change{Record: separation}))
			require.NoError(t, err)
		})
		result, err := service.Organize(context.Background(), f.team, ontology.OrganizationInput{OperationKey: "stale-override", Sources: []ontology.SourceHandle{a, b}})
		require.Error(t, err)
		require.Equal(t, "stale_input", result.FailureCode)
		page, err := f.store.ListRecords(context.Background(), f.team, ontology.EvidenceGroup, "", 20)
		require.NoError(t, err)
		require.Empty(t, page.Records)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
}
