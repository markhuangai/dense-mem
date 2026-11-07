//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	accesspg "github.com/markhuangai/dense-mem/internal/access/postgres"
	auditapp "github.com/markhuangai/dense-mem/internal/audit"
	auditpg "github.com/markhuangai/dense-mem/internal/audit/postgres"
	"github.com/markhuangai/dense-mem/internal/correlation"
	"github.com/markhuangai/dense-mem/internal/domain"
	"github.com/markhuangai/dense-mem/internal/observability"
	application "github.com/markhuangai/dense-mem/internal/ontology"
	contract "github.com/markhuangai/dense-mem/internal/ontology/contract"
	ontologypg "github.com/markhuangai/dense-mem/internal/ontology/postgres"
	operations "github.com/markhuangai/dense-mem/internal/operations"
	operationspg "github.com/markhuangai/dense-mem/internal/operations/postgres"
	"github.com/stretchr/testify/require"
)

func TestOntologySemanticPublicationLogOnlyDiagnostics(t *testing.T) {
	admin, app, rls, cleanup := setupRememberProcessorIntegrationDB(t)
	defer cleanup()
	for _, level := range []slog.Level{observability.LevelTrace, slog.LevelDebug, slog.LevelInfo} {
		t.Run(level.String(), func(t *testing.T) {
			team, owner := uuid.New(), uuid.New()
			require.NoError(t, accesspg.NewTeamRepository(admin, rls).Create(context.Background(), &domain.Team{ID: team, Name: "semantic-logs-" + uuid.NewString()}))
			credential := &domain.Credential{ID: owner, TeamID: team, Role: "manager", Name: "semantic log owner", KeyHash: "synthetic-" + owner.String(), KeyPrefix: strings.ReplaceAll(owner.String(), "-", "")[:24], KeySuffix: "test", Scopes: []string{"read", "write"}}
			require.NoError(t, accesspg.NewCredentialRepository(admin, rls, nil).CreateCredential(context.Background(), credential))
			ctx := rememberProcessorActorContext(t, context.Background(), app, rls, team, owner)
			ctx = correlation.WithID(ctx, "semantic-"+team.String())
			const secret = "semantic-configured-key"
			const auth = "semantic-request-credential"
			ctx = observability.WithAuthenticationSecrets(ctx, auth)
			protector := observability.NewCredentialProtector(secret)
			logs := operations.NewOperationLogService(operationspg.NewOperationLogRepository(app, rls), nil)
			store := ontologypg.NewStore(app, rls)
			var root *observability.Logger
			capture := func(run func()) (string, []map[string]any) {
				return captureOntologyPublicationConsole(t, func() {
					root = observability.NewWithProtector(level, protector)
					require.NoError(t, root.AttachSink(logs))
					store.WithPublicationDiagnostics(ontologyPublicationDiagnostics(root.ContextSlog(), protector))
					run()
				})
			}
			record := func(key, base, parent string, relation contract.ParentRelation) contract.Record {
				return contract.Record{ID: uuid.NewString(), Kind: contract.EntityClass, Definition: &contract.Definition{Key: key, Label: key, Description: "token password secret are admitted fields " + secret + "/" + auth, BaseEntityKind: base, ParentID: parent, ParentRelation: relation}}
			}
			parent := record("parent", "person", "", "")
			child := record("child", "person", parent.ID, contract.SubclassOf)
			publish := func(revision int64, changes ...contract.Change) (contract.PublicationResult, error) {
				return store.PublishManager(ctx, team.String(), contract.Publication{OperationKey: uuid.NewString(), ExpectedRevision: revision, Reason: "Semantic diagnostic acceptance", Changes: changes})
			}
			console, _ := capture(func() {
				result, err := publish(0, contract.Change{Record: parent}, contract.Change{Record: child})
				require.NoError(t, err)
				require.EqualValues(t, 1, result.Revision)
			})
			require.Contains(t, console, "ontology_semantic_publication_committed")
			require.Contains(t, console, "\"transaction_outcome\":\"committed\"")
			for _, test := range []struct {
				name, reason string
				expected     int64
				change       contract.Change
			}{
				{"cycle", "hierarchy_cycle", 1, contract.Change{ExpectedVersion: 1, Record: func() contract.Record {
					value := parent
					definition := *parent.Definition
					definition.ParentID, definition.ParentRelation = child.ID, contract.SubclassOf
					value.Definition = &definition
					return value
				}()}},
				{"incompatible_class", "class_parent_kind", 1, contract.Change{Record: record("wrong-base", "project", parent.ID, contract.SubclassOf)}},
				{"stale_catalog", "catalog_revision", 0, contract.Change{Record: record("stale", "person", parent.ID, contract.SubclassOf)}},
				{"stale_record", "record_version", 1, contract.Change{ExpectedVersion: 0, Record: child}},
				{"unknown_relation", "parent_relation", 1, contract.Change{Record: record("unknown-relation", "person", parent.ID, "unknown")}},
				{"wrong_relation_kind", "parent_relation_kind", 1, contract.Change{Record: record("wrong-relation-kind", "person", parent.ID, contract.SubpropertyOf)}},
				{"missing_parent", "parent_required", 1, contract.Change{Record: record("missing-parent", "person", "", contract.SubclassOf)}},
				{"unavailable_parent", "parent_unavailable", 1, contract.Change{Record: record("unavailable-parent", "person", uuid.NewString(), contract.SubclassOf)}},
				{"wrong_parent_kind", "parent_kind", 1, contract.Change{Record: contract.Record{ID: uuid.NewString(), Kind: contract.PredicateConcept, Definition: &contract.Definition{Key: "wrong-parent-kind", Label: "Wrong parent kind", ParentID: parent.ID, ParentRelation: contract.SubpropertyOf}}}},
			} {
				t.Run(test.name, func(t *testing.T) {
					console, records := capture(func() { _, err := publish(test.expected, test.change); require.Error(t, err) })
					require.Contains(t, console, "ontology_semantic_publication_rejected")
					require.Contains(t, console, test.reason)
					require.NotContains(t, console, "ontology_semantic_publication_committed")
					require.NotContains(t, console, secret)
					require.NotContains(t, console, auth)
					var operation string
					for _, entry := range records {
						require.Equal(t, team.String(), entry["team_id"])
						require.Equal(t, owner.String(), entry["profile_id"])
						require.Equal(t, "semantic-"+team.String(), entry["correlation_id"])
						if operation == "" {
							operation = entry["operation_id"].(string)
						}
						require.Equal(t, operation, entry["operation_id"])
					}
					if level <= slog.LevelDebug {
						bodies := ontologyPublicationPayloads(t, records)
						var rejection contract.PublicationRejection
						require.NoError(t, json.Unmarshal(bodies["rejection:"], &rejection))
						require.Equal(t, test.reason, rejection.Reason)
						if strings.HasPrefix(test.name, "stale_") {
							require.Equal(t, "0", rejection.Expected)
							require.Equal(t, "1", rejection.Observed)
						}
						if test.name == "cycle" || test.name == "incompatible_class" {
							require.Contains(t, bodies, "definition:"+parent.ID)
						}
					} else {
						require.NotContains(t, console, "payload_chunk")
					}
				})
			}
			service := application.NewManagementService(store, auditapp.New(auditpg.NewStore(app, rls)))
			draft, err := service.SaveDraft(ctx, "", contract.SaveDraftInput{OperationKey: uuid.NewString(), ExpectedRevision: 1, Reason: "Audit failure acceptance", Edits: []contract.ManagementEdit{{Action: "define", Kind: contract.EntityClass, Definition: record("audit-child", "person", parent.ID, contract.SubclassOf).Definition}}})
			require.NoError(t, err)
			require.NoError(t, admin.Exec(`CREATE FUNCTION reject_semantic_publication_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.operation='ONTOLOGY_PUBLISH' THEN RAISE EXCEPTION 'fixture semantic audit rejection'; END IF; RETURN NEW; END $$;
				CREATE TRIGGER reject_semantic_publication_audit BEFORE INSERT ON audit_log FOR EACH ROW EXECUTE FUNCTION reject_semantic_publication_audit()`).Error)
			t.Cleanup(func() {
				require.NoError(t, admin.Exec(`DROP TRIGGER IF EXISTS reject_semantic_publication_audit ON audit_log; DROP FUNCTION IF EXISTS reject_semantic_publication_audit()`).Error)
			})
			console, _ = capture(func() {
				service.WithPublicationDiagnostics(ontologyPublicationDiagnostics(root.ContextSlog(), protector))
				_, err := service.CommandDraft(ctx, draft.ID, "publish", contract.DraftCommand{OperationKey: uuid.NewString(), ExpectedVersion: draft.Version, ExpectedRevision: 1, Reason: draft.Reason})
				require.ErrorIs(t, err, contract.ErrAuditUnavailable)
			})
			require.NoError(t, admin.Exec(`DROP TRIGGER reject_semantic_publication_audit ON audit_log; DROP FUNCTION reject_semantic_publication_audit()`).Error)
			require.Contains(t, console, "ontology_semantic_publication_failed")
			require.Contains(t, console, "fixture semantic audit rejection")
			require.NotContains(t, console, "ontology_semantic_publication_committed")
			unchanged, err := service.Draft(ctx, draft.ID)
			require.NoError(t, err)
			require.Equal(t, draft, unchanged)
			require.NoError(t, admin.Exec(`CREATE FUNCTION reject_semantic_publication_commit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture semantic commit rejection'; END $$;
				CREATE CONSTRAINT TRIGGER reject_semantic_publication_commit AFTER INSERT ON ontology_publications DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION reject_semantic_publication_commit()`).Error)
			t.Cleanup(func() {
				require.NoError(t, admin.Exec(`DROP TRIGGER IF EXISTS reject_semantic_publication_commit ON ontology_publications; DROP FUNCTION IF EXISTS reject_semantic_publication_commit()`).Error)
			})
			console, _ = capture(func() {
				_, err := publish(1, contract.Change{Record: record("commit-child", "person", parent.ID, contract.SubclassOf)})
				require.ErrorContains(t, err, "fixture semantic commit rejection")
			})
			require.NoError(t, admin.Exec(`DROP TRIGGER reject_semantic_publication_commit ON ontology_publications; DROP FUNCTION reject_semantic_publication_commit()`).Error)
			require.Contains(t, console, "ontology_semantic_publication_failed")
			require.NotContains(t, console, "ontology_semantic_publication_committed")
			page, err := store.ListRecords(ctx, team.String(), "", "", 200)
			require.NoError(t, err)
			require.EqualValues(t, 1, page.Revision)
			require.Len(t, page.Records, 2)
			var publications int
			require.NoError(t, admin.Raw(`SELECT count(*) FROM ontology_publications WHERE team_id=?::uuid`, team).Row().Scan(&publications))
			require.Equal(t, 1, publications)
			stored, err := logs.ListOperationLogs(context.Background(), domain.OperationLogFilter{Event: "ontology_semantic_publication_failed", TeamID: &team})
			require.NoError(t, err)
			require.EqualValues(t, 2, stored.Total)
			foreign := uuid.New()
			invisible, err := logs.ListOperationLogs(context.Background(), domain.OperationLogFilter{Event: "ontology_semantic_publication_failed", TeamID: &foreign})
			require.NoError(t, err)
			require.Zero(t, invisible.Total)
			capture(func() {
				pin := contract.Record{ID: uuid.NewString(), Kind: contract.OverrideKind, Override: &contract.Override{Action: contract.PinDefinition, TargetID: child.ID}}
				_, err := publish(1, contract.Change{Record: pin})
				require.NoError(t, err)
			})
			console, records := capture(func() {
				_, err := store.PublishAutomatic(context.Background(), team.String(), contract.Publication{OperationKey: uuid.NewString(), ExpectedRevision: 2, Reason: "Pinned semantic definition", Changes: []contract.Change{{ExpectedVersion: 1, Record: child}}})
				require.ErrorIs(t, err, contract.ErrOverride)
			})
			require.Contains(t, console, "definition_pinned")
			if level <= slog.LevelDebug {
				ontologyPublicationPayloads(t, records)
			}
			depthParent, depthRevision := "", int64(2)
			var changes []contract.Change
			for depth := 0; depth <= contract.MaxParentDepth; depth++ {
				relation := contract.SubclassOf
				if depthParent == "" {
					relation = ""
				}
				value := record(fmt.Sprintf("depth-%d", depth), "person", depthParent, relation)
				depthParent = value.ID
				changes = append(changes, contract.Change{Record: value})
				if len(changes) == 16 || depth == contract.MaxParentDepth {
					capture(func() {
						result, err := publish(depthRevision, changes...)
						require.NoError(t, err)
						depthRevision = result.Revision
					})
					changes = nil
				}
			}
			console, records = capture(func() {
				_, err := publish(depthRevision, contract.Change{Record: record("depth-rejected", "person", depthParent, contract.SubclassOf)})
				require.ErrorIs(t, err, contract.ErrInvalid)
			})
			require.Contains(t, console, "hierarchy_depth")
			require.NotContains(t, console, "ontology_semantic_publication_committed")
			if level <= slog.LevelDebug {
				ontologyPublicationPayloads(t, records)
			}
		})
	}
}
