//go:build integration

package postgres

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	access "github.com/markhuangai/dense-mem/internal/access/postgres"
	"github.com/markhuangai/dense-mem/internal/domain"
	knowledge "github.com/markhuangai/dense-mem/internal/knowledge/contract"
	"github.com/markhuangai/dense-mem/internal/requestctx"
	storage "github.com/markhuangai/dense-mem/internal/storage/postgres"
	"github.com/markhuangai/dense-mem/internal/topics/assessment"
	"github.com/markhuangai/dense-mem/internal/topics/assignment"
	topiccontract "github.com/markhuangai/dense-mem/internal/topics/contract"
	"github.com/stretchr/testify/require"
)

func testOntologyOrganizationVocabularyAndSourceFences(t *testing.T) {
	t.Run("oversized dependency context records a zero-call failure", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		dependency := f.source(t, source)
		revision := int64(0)
		for offset := 0; offset < topiccontract.MaxDependencyRecords; offset += topiccontract.MaxChanges {
			var changes []topiccontract.Change
			for i := offset; i < offset+topiccontract.MaxChanges && i < topiccontract.MaxDependencyRecords; i++ {
				record := testTopic(fmt.Sprintf("context-%d", i))
				record.Sources = []topiccontract.SourceDependency{dependency}
				changes = append(changes, topiccontract.Change{Record: record})
			}
			result, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication(fmt.Sprintf("context-batch-%d", offset), revision, changes...))
			require.NoError(t, err)
			revision = result.Revision
		}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("extra-vocabulary", revision, topiccontract.Change{Record: testTopic("postgresql")}))
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		service, calls := organizationFixtureService(t, f, nil, nil)
		input := topiccontract.OrganizationInput{OperationKey: "oversized-dependencies", Sources: []topiccontract.SourceHandle{source}}
		result, err := service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, topiccontract.ErrContextBound)
		require.Equal(t, "organization_context_bound", result.FailureCode)
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
		extra.Sources = []topiccontract.SourceDependency{dependency}
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("source-dependent-overflow", revision+1, topiccontract.Change{Record: extra}))
		require.NoError(t, err)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{source})
		require.ErrorIs(t, err, topiccontract.ErrContextBound)
		require.Len(t, contextData.Sources, 1)
		require.Empty(t, contextData.Records)
		var historyCount int64
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_record_revisions WHERE team_id=?::uuid`, f.team).Row().Scan(&historyCount))
		require.Equal(t, int64(topiccontract.MaxDependencyRecords+2), historyCount)
		input.OperationKey = "source-dependent-overflow"
		result, err = service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, topiccontract.ErrContextBound)
		require.Equal(t, "organization_context_bound", result.FailureCode)
		require.False(t, result.Current)
		require.Nil(t, result.Publication)
		require.Empty(t, result.Attempts)
		require.Equal(t, []topiccontract.OrganizationOutcome{{Source: source, Status: "failed", Reason: "organization_context_bound"}}, result.Outcomes)
		require.Equal(t, 2, receiptCount(t, f))
		replay, err = service.Organize(context.Background(), f.team, input)
		require.Error(t, err)
		require.True(t, replay.Existing)
		require.False(t, replay.Current)
		require.Equal(t, result.AssessmentID, replay.AssessmentID)
		require.Equal(t, result.Outcomes, replay.Outcomes)
		input.Sources = []topiccontract.SourceHandle{source, {Kind: topiccontract.EvidenceSource, ID: uuid.NewString(), Version: 1}}
		_, err = service.Organize(context.Background(), f.team, input)
		require.ErrorIs(t, err, topiccontract.ErrConflict)
		require.Equal(t, 2, receiptCount(t, f))
		var unchangedHistoryCount int64
		require.NoError(t, f.admin.Raw(`SELECT count(*) FROM topic_record_revisions WHERE team_id=?::uuid`, f.team).Row().Scan(&unchangedHistoryCount))
		require.Equal(t, historyCount, unchangedHistoryCount)
		require.Zero(t, calls.Load())
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("historical pin is retained without entering vocabulary", func(t *testing.T) {
		ctx := context.Background()
		f := newHistoricalOntologyFixture(t)
		migrator, err := storage.NewMigrator(f.admin)
		require.NoError(t, err)
		require.NoError(t, migrator.RunUpThroughEvidenceOnly(ctx))
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		topic := testTopic("postgresql")
		pin := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.OverrideKind, Override: &topiccontract.Override{Action: topiccontract.PinDefinition, TargetID: topic.ID}}
		original := seedHistoricalRecords(t, f, "pinned-vocabulary", topic, pin)
		before := f.canonicalSnapshot(t)
		require.NoError(t, migrator.RunUp(ctx))
		historical, err := f.store.GetRecord(f.actor(0, "manager"), f.team, pin.ID, 1)
		require.NoError(t, err)
		require.Equal(t, original[pin.ID], historical.Record)
		contextData, err := f.store.ReadOrganization(ctx, f.team, []topiccontract.SourceHandle{source})
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, 1)
		require.Equal(t, topic.ID, contextData.Candidates[0].ID)
		require.Empty(t, contextData.Records)
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
			for i, source := range []topiccontract.SourceHandle{a, b} {
				_, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: fmt.Sprintf("classify-relative-%d", i), Sources: []topiccontract.SourceHandle{source}})
				require.NoError(t, err)
			}
			before := f.canonicalSnapshot(t)
			priorCalls := calls.Load()
			result, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "compare-relative", Sources: []topiccontract.SourceHandle{a, b}})
			require.NoError(t, err)
			require.Equal(t, priorCalls+1, calls.Load())
			require.Len(t, result.Attempts, 1)
			groups, err := f.store.ListRecords(context.Background(), f.team, topiccontract.EvidenceGroup, "", 20)
			require.NoError(t, err)
			require.Empty(t, groups.Records)
			if relation == "ambiguous" {
				require.Len(t, result.AmbiguousComparisons, 1)
			}
			require.Equal(t, before, f.canonicalSnapshot(t))
		})
	}
	for _, match := range []string{"key", "label", "alias"} {
		t.Run("reuse omitted current definition/"+match, func(t *testing.T) {
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
			_, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "current-definition", Sources: []topiccontract.SourceHandle{oldSource}})
			require.NoError(t, err)
			id := topiccontract.OrganizationRecordID(f.team, topiccontract.Topic, "postgresql")
			definition, err := f.store.GetRecord(context.Background(), f.team, id, 0)
			require.NoError(t, err)
			require.True(t, definition.Current)
			newSource := f.organizationEvidence(t, 1, newText, nil)
			contextData, err := f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{newSource})
			require.NoError(t, err)
			require.Empty(t, contextData.Candidates)
			before := f.canonicalSnapshot(t)
			input := topiccontract.OrganizationInput{OperationKey: "reuse-current-definition", Sources: []topiccontract.SourceHandle{newSource}}
			result, err := service.Organize(context.Background(), f.team, input)
			require.NoError(t, err)
			require.NotNil(t, result.Publication)
			reused, err := f.store.GetRecord(context.Background(), f.team, id, 0)
			require.NoError(t, err)
			require.True(t, reused.Current)
			require.Equal(t, definition.Record, reused.Record)
			if match != "key" {
				_, err = f.store.GetRecord(context.Background(), f.team, topiccontract.OrganizationRecordID(f.team, topiccontract.Topic, "relational-store"), 0)
				require.ErrorIs(t, err, topiccontract.ErrNotFound)
			}
			for _, source := range []topiccontract.SourceHandle{oldSource, newSource} {
				assignment, err := f.store.GetRecord(context.Background(), f.team, topiccontract.OrganizationRecordID(f.team, topiccontract.AssignmentKind, topiccontract.SourceKey(source)), 0)
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
	t.Run("definition name lookup is team scoped", func(t *testing.T) {
		f := newOrganizationFixture(t)
		topic := testTopic("postgresql")
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("same-name-different-kinds", 0, topiccontract.Change{Record: topic}))
		require.NoError(t, err)
		query := topiccontract.Record{ID: topiccontract.OrganizationRecordID(f.team, topiccontract.Topic, "relational-store"), Kind: topiccontract.Topic, Definition: &topiccontract.Definition{Key: "relational-store", Label: "Relational storage", Aliases: []string{" POSTGRESQL "}}}
		heads, err := f.store.ReadDefinitionHeads(context.Background(), f.team, query)
		require.NoError(t, err)
		require.Len(t, heads, 1)
		require.Equal(t, topic.ID, heads[0].ID)
		_, err = f.store.ReadDefinitionHeads(f.actor(1, "member"), f.team, query)
		require.ErrorIs(t, err, topiccontract.ErrUnauthorized)
		other := &domain.Team{Name: "definition-name-c-" + uuid.NewString()}
		require.NoError(t, access.NewTeamRepository(f.admin, f.rls).Create(context.Background(), other))
		heads, err = f.store.ReadDefinitionHeads(context.Background(), other.ID.String(), query)
		require.NoError(t, err)
		require.Empty(t, heads)
	})
	t.Run("withdrawn member does not block remaining source", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		memberOwner, memberText := 1, "PostgreSQL is Atlas's data store."
		b := f.organizationEvidence(t, memberOwner, memberText, nil)
		require.NotEqual(t, a.ID, b.ID)
		service, calls := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, nil)
		_, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "original-group", Sources: []topiccontract.SourceHandle{a, b}})
		require.NoError(t, err)
		groups, err := f.store.ListRecords(context.Background(), f.team, topiccontract.EvidenceGroup, "", 20)
		require.NoError(t, err)
		require.Len(t, groups.Records, 1)
		group := groups.Records[0].Record
		_, err = f.knowledge.RetractEvidence(f.actor(memberOwner, "member"), knowledge.RetractEvidenceInput{TeamID: f.team, OwnerProfileID: f.owners[memberOwner], EvidenceIDs: []string{b.ID}, Reason: "withdraw grouped member", IdempotencyKey: "withdraw-member", RequestHash: testHash("withdraw-member")})
		require.NoError(t, err)
		unrelated := f.organizationEvidence(t, 0, "An unrelated PostgreSQL note.", nil)
		before := f.canonicalSnapshot(t)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{a})
		require.NoError(t, err)
		found := false
		for _, view := range contextData.Records {
			if view.ID == group.ID {
				found = true
				require.False(t, view.Current)
			}
		}
		require.True(t, found)
		require.Len(t, contextData.Candidates, 1)
		require.False(t, contextData.Candidates[0].Current)
		unrelatedContext, err := f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{unrelated})
		require.NoError(t, err)
		require.Empty(t, unrelatedContext.Candidates)
		priorCalls := calls.Load()
		input := topiccontract.OrganizationInput{OperationKey: "remaining-source", Sources: []topiccontract.SourceHandle{a}}
		result, err := service.Organize(context.Background(), f.team, input)
		require.NoError(t, err)
		require.Equal(t, "unchanged", result.Outcomes[0].Status)
		require.Nil(t, result.Publication)
		require.Equal(t, priorCalls, calls.Load())
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
	t.Run("affected membership revisions reuse matching topics", func(t *testing.T) {
		f := newOrganizationFixture(t)
		var handles []topiccontract.SourceHandle
		var definitions, assignments, refinements []topiccontract.Change
		var targets []topiccontract.Record
		oldBySource := map[string]topiccontract.Record{}
		targetBySource := map[string]topiccontract.Record{}
		textBySource := map[string]string{}
		for i := range 11 {
			text := fmt.Sprintf("Record number %d uses an isolated database.", i)
			source := f.organizationEvidence(t, i%2, text, nil)
			handles = append(handles, source)
			old := testTopic(fmt.Sprintf("old-category-%d", i))
			old.ID = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
			manager := testTopic(fmt.Sprintf("manager-category-%d", i))
			manager.ID = fmt.Sprintf("ffffffff-ffff-4fff-8fff-%012d", i+1)
			manager.Definition.Description = text
			oldBySource[source.ID], targetBySource[source.ID], textBySource[source.ID] = old, manager, text
			targets = append(targets, manager)
			definitions = append(definitions, topiccontract.Change{Record: old}, topiccontract.Change{Record: manager})
			dependency := f.source(t, source)
			assignment := topiccontract.Record{ID: topiccontract.OrganizationRecordID(f.team, topiccontract.AssignmentKind, topiccontract.SourceKey(source)), Kind: topiccontract.AssignmentKind, Assignment: &topiccontract.Assignment{Source: source, DefinitionID: old.ID}, Sources: []topiccontract.SourceDependency{dependency}}
			assignments = append(assignments, topiccontract.Change{Record: assignment})
			refined := old
			refined.Definition = &topiccontract.Definition{Key: old.Definition.Key, Label: "Revised " + old.Definition.Label}
			refinements = append(refinements, topiccontract.Change{Record: refined, ExpectedVersion: 1})
		}
		definitionsResult, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("vocabulary-definitions", 0, definitions...))
		require.NoError(t, err)
		assignmentResult, err := f.store.PublishAutomatic(context.Background(), f.team, testPublication("old-assignments", definitionsResult.Revision, assignments...))
		require.NoError(t, err)
		window := maintenanceWindow(t, f, maintenanceSettings(t, f))
		turn, claim := maintenanceClaim(t, f, window)
		require.Len(t, claim.Sources, len(handles))
		initial := []topiccontract.OrganizationOutcome{}
		for _, source := range claim.Sources {
			old := oldBySource[source.ID]
			dependency := f.source(t, source)
			outcome, err := f.store.CommitMaintenanceItemAssignment(topiccontract.WithMaintenanceClaim(context.Background(), *claim), *claim, assignment.Publication{
				EvidenceID: source.ID, ExpectedFingerprint: dependency.Fingerprint,
				Outcome: assignment.Outcome{Text: textBySource[source.ID], Method: "generation", InputHash: testHash("initial-" + source.ID), Matches: []assignment.Match{{ID: old.ID, Version: 1}}},
			})
			require.NoError(t, err)
			initial = append(initial, outcome)
		}
		require.NoError(t, f.store.CompleteMaintenanceBatch(context.Background(), *claim, topiccontract.OrganizationResult{Current: true, IsolatedOutcomes: true, Outcomes: initial}, "", time.Now()))
		require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
		priorRevisions := map[string]int64{}
		for _, source := range handles {
			state := revalidationQueue(t, f, source)
			require.Equal(t, "organized", state.Status)
			priorRevisions[source.ID] = state.Revision
		}
		_, err = f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("revised-topics", assignmentResult.Revision, refinements...))
		require.NoError(t, err)
		before := f.canonicalSnapshot(t)
		revalidationDiscover(t, f, window)
		original := map[string]topiccontract.Record{}
		for i, prior := range assignments {
			view, err := f.store.GetRecord(context.Background(), f.team, prior.Record.ID, 0)
			require.NoError(t, err)
			require.True(t, view.Current)
			original[view.ID] = view.Record
			state := revalidationQueue(t, f, handles[i])
			require.Equal(t, "pending", state.Status)
			require.Greater(t, state.Revision, priorRevisions[handles[i].ID])
		}
		turn, claim = maintenanceClaim(t, f, window)
		require.Len(t, claim.Sources, len(handles))
		outcomes := []topiccontract.OrganizationOutcome{}
		for _, source := range claim.Sources {
			dependency := f.source(t, source)
			scope := assignment.Scope{TeamID: f.team, SpaceID: f.space, Generation: f.generation, OperationID: claim.ID}
			candidates, err := f.store.ReadAssignmentCandidates(context.Background(), scope, assignment.Slice{EvidenceID: source.ID, SourceFingerprint: dependency.Fingerprint, Text: textBySource[source.ID]})
			require.NoError(t, err)
			require.True(t, candidates.NeedsRepair)
			require.LessOrEqual(t, len(candidates.Topics), assignment.MaxTopics)
			require.Equal(t, oldBySource[source.ID].ID, candidates.Topics[0].ID)
			target := targetBySource[source.ID]
			found := false
			for _, candidate := range candidates.Topics {
				found = found || candidate.ID == target.ID
			}
			require.True(t, found)
			outcome, err := f.store.CommitMaintenanceItemAssignment(topiccontract.WithMaintenanceClaim(context.Background(), *claim), *claim, assignment.Publication{
				EvidenceID: source.ID, ExpectedFingerprint: dependency.Fingerprint,
				Outcome: assignment.Outcome{Text: textBySource[source.ID], Method: "generation", InputHash: testHash("repair-" + source.ID), Reviewed: candidates.Topics, Matches: []assignment.Match{{ID: target.ID, Version: 1}}},
			})
			require.NoError(t, err)
			require.Equal(t, "organized", outcome.Status)
			outcomes = append(outcomes, outcome)
		}
		require.NoError(t, f.store.CompleteMaintenanceBatch(context.Background(), *claim, topiccontract.OrganizationResult{Current: true, IsolatedOutcomes: true, Outcomes: outcomes}, "", time.Now()))
		require.NoError(t, f.store.ReleaseMaintenanceTurn(context.Background(), *turn))
		for i, prior := range assignments {
			view, err := f.store.GetRecord(context.Background(), f.team, prior.Record.ID, 0)
			require.NoError(t, err)
			require.True(t, view.Retired)
			require.Equal(t, int64(2), view.Version)
			history, err := f.store.GetRecord(context.Background(), f.team, view.ID, 1)
			require.NoError(t, err)
			require.Equal(t, original[view.ID], history.Record)
			id := topiccontract.OrganizationRecordID(f.team, topiccontract.AssignmentKind, topiccontract.SourceKey(handles[i])+":"+targets[i].ID)
			current, err := f.store.GetRecord(context.Background(), f.team, id, 0)
			require.NoError(t, err)
			require.True(t, current.Current)
			require.Equal(t, targets[i].ID, current.Assignment.DefinitionID)
		}
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
	t.Run("bounded vocabulary reuse", func(t *testing.T) {
		f := newOrganizationFixture(t)
		source := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		changes := []topiccontract.Change{}
		for i := 0; i < 24; i++ {
			changes = append(changes, topiccontract.Change{Record: testTopic(fmt.Sprintf("PostgreSQL memory %d", i))})
		}
		_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("vocabulary", 0, changes...))
		require.NoError(t, err)
		contextData, err := f.store.ReadOrganization(context.Background(), f.team, []topiccontract.SourceHandle{source})
		require.NoError(t, err)
		require.Len(t, contextData.Candidates, 20)
		service, _ := organizationFixtureService(t, f, nil, func(request assessment.Request, response *assessment.Response) {
			require.Len(t, request.Definitions, 20)
			require.Empty(t, response.Definitions)
		})
		_, err = service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "reuse", Sources: []topiccontract.SourceHandle{source}})
		require.NoError(t, err)
		page, err := f.store.ListRecords(context.Background(), f.team, topiccontract.Topic, "", 200)
		require.NoError(t, err)
		require.Len(t, page.Records, 24)
	})
	t.Run("private and old generation", func(t *testing.T) {
		f := newOrganizationFixture(t)
		private := &domain.Credential{ID: uuid.New(), TeamID: uuid.MustParse(f.team), Name: "private", KeyHash: "synthetic-private", KeyPrefix: uuid.NewString()[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
		require.NoError(t, access.NewCredentialRepository(f.admin, f.rls, nil).CreateCredential(context.Background(), private))
		privateCtx := requestctx.WithAllowedSpaces(context.Background(), []domain.MemorySpaceAccess{{ID: private.MemorySpaceID, Kind: domain.MemorySpacePrivate, Generation: private.MemorySpaceGeneration}})
		stored, err := f.knowledge.CreateIngestForTest(privateCtx, knowledge.CreateIngestInput{TeamID: f.team, OwnerProfileID: private.ID.String(), SpaceID: private.MemorySpaceID.String(), SpaceGeneration: private.MemorySpaceGeneration, Evidence: []knowledge.EvidenceInput{{Content: "Private source"}}})
		require.NoError(t, err)
		service, calls := organizationFixtureService(t, f, nil, nil)
		privateHandle := topiccontract.SourceHandle{Kind: topiccontract.EvidenceSource, ID: stored.Evidence[0].FragmentID, Version: 1}
		result, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "private", Sources: []topiccontract.SourceHandle{privateHandle}})
		require.NoError(t, err)
		require.Equal(t, "unavailable", result.Outcomes[0].Status)
		require.Zero(t, calls.Load())
		source := f.organizationEvidence(t, 0, "Shared old generation", nil)
		require.NoError(t, f.admin.Exec(`UPDATE memory_spaces SET generation=generation+1 WHERE team_id=?::uuid AND kind='team_shared'`, f.team).Error)
		result, err = service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "old-generation", Sources: []topiccontract.SourceHandle{source}})
		require.NoError(t, err)
		require.Equal(t, "unavailable", result.Outcomes[0].Status)
		require.Zero(t, calls.Load())
	})
	t.Run("retired override cannot intercept assessment", func(t *testing.T) {
		f := newOrganizationFixture(t)
		a := f.organizationEvidence(t, 0, "Atlas uses PostgreSQL.", nil)
		b := f.organizationEvidence(t, 1, "Atlas uses PostgreSQL.", nil)
		before := f.canonicalSnapshot(t)
		service, _ := organizationFixtureService(t, f, func(assessment.Item, assessment.Item) bool { return true }, func(request assessment.Request, response *assessment.Response) {
			separation := topiccontract.Record{ID: uuid.NewString(), Kind: topiccontract.OverrideKind, Override: &topiccontract.Override{Action: topiccontract.KeepSeparate, Members: []topiccontract.SourceHandle{a, b}}, Sources: []topiccontract.SourceDependency{f.source(t, a), f.source(t, b)}}
			_, err := f.store.PublishManager(f.actor(0, "manager"), f.team, testPublication("concurrent-separation", 0, topiccontract.Change{Record: separation}))
			require.ErrorIs(t, err, topiccontract.ErrOverride)
		})
		result, err := service.Organize(context.Background(), f.team, topiccontract.OrganizationInput{OperationKey: "retired-override", Sources: []topiccontract.SourceHandle{a, b}})
		require.NoError(t, err)
		require.True(t, result.Current)
		page, err := f.store.ListRecords(context.Background(), f.team, topiccontract.EvidenceGroup, "", 20)
		require.NoError(t, err)
		require.Len(t, page.Records, 1)
		require.Equal(t, before, f.canonicalSnapshot(t))
	})
}
