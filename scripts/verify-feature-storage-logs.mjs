import assert from 'node:assert/strict';

const outcome = process.argv[2];
assert.ok(['failed', 'resumed'].includes(outcome), 'expected failed or resumed outcome');
let input = '';
for await (const chunk of process.stdin) input += chunk;
const events = input.split('\n').flatMap(line => {
  try { return [JSON.parse(line)]; } catch { return []; }
});
const migration = events.filter(event => event.msg?.startsWith('feature_storage_migration_'));
assert.ok(migration.length, 'complete server stdout must contain feature storage events');
assert.ok(!JSON.stringify(migration).includes('Exact progress Evidence.'), 'migration logs must exclude memory text');
const checkpoint = migration.find(event => event.phase === 'copy' && event.work_unit === 'ontology_record_revisions'
  && event.state === (outcome === 'failed' ? 'batch_committed' : 'resumed') && event.committed_rows === 100);
assert.ok(checkpoint, 'logs must identify the 100-row durable copy checkpoint');
assert.equal(checkpoint.level, 'INFO');
assert.equal(checkpoint.transaction_outcome, 'committed');
for (const key of ['migration_execution_id', 'migration_version', 'inventory_identity', 'source_fingerprint', 'checkpoint_fingerprint']) {
  assert.ok(checkpoint[key], `checkpoint requires ${key}`);
}
if (outcome === 'failed') {
  const failure = migration.find(event => event.msg === 'feature_storage_migration_failed');
  assert.ok(failure, 'required failure must be visible in stdout');
  assert.equal(failure.level, 'ERROR');
  assert.equal(failure.failure_reason, 'feature_storage_destination_payload_mismatch');
  assert.equal(failure.transaction_outcome, 'rolled_back');
  assert.equal(failure.capture_state, 'incomplete');
  assert.equal(failure.recovery_action, 'resume_matching_application_after_resolving_reported_cause');
  assert.ok(!events.some(event => event.msg === 'server_started'), 'failed startup must not report a listener');
} else {
  const complete = migration.find(event => event.phase === 'cutover' && event.state === 'completed');
  assert.ok(complete, 'recovery must record verified cutover');
  assert.equal(complete.level, 'INFO');
  assert.equal(complete.transaction_outcome, 'committed');
  assert.equal(complete.capture_state, 'complete');
}
process.stdout.write(`${JSON.stringify({ scenario: 'ontology_upgrade', feature_storage_log_diagnosis: outcome,
  checkpoint_rows: checkpoint.committed_rows, execution_id: checkpoint.migration_execution_id })}\n`);
