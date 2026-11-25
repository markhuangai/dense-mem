import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';

const outcome = process.argv[2];
assert.ok(['failed', 'resumed', 'relational-failed', 'relational-resumed'].includes(outcome), 'expected failed or resumed outcome');
let input = '';
for await (const chunk of process.stdin) input += chunk;
const events = input.split('\n').flatMap(line => {
  try { return [JSON.parse(line)]; } catch { return []; }
});
if (outcome.startsWith('relational-')) {
  const rows = events.filter(event => event.msg?.startsWith('relational_topic_migration_'));
  const stopped = outcome === 'relational-failed';
  const checkpoint = rows.find(event => event.phase === 'copy' &&
    event.state === (stopped ? 'batch_committed' : 'resumed') && event.committed_rows === 100);
  const terminal = rows.find(event => stopped ? event.state === 'failed' : event.state === 'verified');
  assert.ok(checkpoint && terminal, 'relational copy checkpoint and terminal event are required');
  assert.equal(checkpoint.migration_version, 20261125010001);
  assert.equal(checkpoint.executor_version, 'relational-topics-v1');
  assert.equal(checkpoint.work_unit, 'current_topic_records');
  assert.equal(checkpoint.migration_execution_id, terminal.migration_execution_id);
  assert.equal(checkpoint.transaction_outcome, 'committed');
  assert.match(checkpoint.checkpoint_fingerprint, /^[0-9a-f]{64}$/);
  assert.ok(terminal.phase_elapsed_ms >= 0);
  if (stopped) {
    assert.equal(terminal.failure_reason, 'relational_topics_destination_write_rejected');
    assert.equal(terminal.transaction_outcome, 'rolled_back');
    assert.equal(terminal.committed_rows, 100);
  } else {
    assert.equal(terminal.transaction_outcome, 'committed');
    assert.equal(terminal.committed_rows, 210);
    assert.match(terminal.checkpoint_fingerprint, /^[0-9a-f]{64}$/);
  }
  const digest = createHash('sha256').update(JSON.stringify([checkpoint, terminal])).digest('hex');
  process.stdout.write(JSON.stringify({
    scenario: 'ontology_upgrade', migration: 'relational-topics-v1', execution_id: terminal.migration_execution_id,
    batch_count: terminal.committed_batches, committed_rows: terminal.committed_rows, phase_elapsed_ms: terminal.phase_elapsed_ms,
    cause: terminal.failure_reason, corrective_action: terminal.recovery_action, outcome: terminal.transaction_outcome,
    chunk_count: 0, verified_sha256: digest,
  }) + '\n');
  process.exit(0);
}
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
  assert.ok(!events.some(event => event.msg === 'starting server'), 'failed startup must not report a listener');
} else {
  const complete = migration.find(event => event.phase === 'cutover' && event.state === 'completed');
  assert.ok(complete, 'recovery must record verified cutover');
  assert.equal(complete.level, 'INFO');
  assert.equal(complete.transaction_outcome, 'committed');
  if (complete.capture_state === 'incomplete') {
    assert.equal(complete.capture_reason, 'wait_observation_unavailable');
    assert.ok(migration.some(event => event.msg === 'feature_storage_migration_capture_incomplete'
      && event.migration_execution_id === complete.migration_execution_id && event.level === 'WARN'
      && event.capture_reason === complete.capture_reason), 'incomplete capture requires its warning');
  } else {
    assert.equal(complete.capture_state, 'complete');
    assert.equal(complete.capture_reason, '');
  }
}
process.stdout.write(`${JSON.stringify({ scenario: 'ontology_upgrade', feature_storage_log_diagnosis: outcome,
  checkpoint_rows: checkpoint.committed_rows, execution_id: checkpoint.migration_execution_id,
  migration_version: checkpoint.migration_version, committed_batches: checkpoint.committed_batches,
  chunk_count: 0, verified_sha256: createHash('sha256').update(JSON.stringify(migration)).digest('hex') })}\n`);
