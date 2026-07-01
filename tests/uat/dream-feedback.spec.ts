import { test, expect } from '@playwright/test';
import {
  BASE_URL,
  PROFILE_ID,
  headers,
  neo4jQuery,
} from './helpers';

test('dream confirmation enters remember flow and removes processed dream', async ({
  request,
}) => {
  const unique = Date.now();
  const dreamId = `uat-dream-confirm-${unique}`;
  const feedback = `User works on dense-mem dream resolution UAT ${unique}.`;

  await neo4jQuery(
    `
    CREATE (d:Dream {
      team_id: $profileId,
      dream_id: $dreamId,
      hypothesis: $hypothesis,
      what_if: 'What if this confirmed dream can become normal memory evidence?',
      possible_outcome: 'The memory verifier can place the evidence as fragment, claim, or fact.',
      rationale: 'Seeded by UAT to verify dream feedback resolution.',
      likelihood: 0.72,
      confidence: 0.81,
      status: 'proposed',
      cycle: 'dream',
      cycle_run_id: 'uat-dream-feedback',
      generator_model: 'uat',
      content_hash: $dreamId,
      source_refs_json: '[]',
      invalidated_reason: '',
      created_at: datetime(),
      updated_at: datetime(),
      last_evaluated_at: datetime()
    })
    `,
    {
      profileId: PROFILE_ID,
      dreamId,
      hypothesis: `User may work on dense-mem dream resolution UAT ${unique}.`,
    },
  );

  const res = await request.post(`${BASE_URL}/api/v1/tools/resolve_dream_feedback`, {
    headers: headers(PROFILE_ID),
    data: {
      dream_id: dreamId,
      decision: 'confirm_true',
      feedback,
    },
  });

  expect(res.status()).toBe(200);
  const body = await res.json();
  expect(body).toMatchObject({
    deleted: true,
    dream: {
      dream_id: dreamId,
      status: 'promoted',
    },
    memory: {
      ingest_id: expect.any(String),
      status: 'queued',
    },
  });
  expect(body.memory.evidence[0].id).toEqual(expect.any(String));

  const remainingDreams = await neo4jQuery(
    `
    MATCH (d:Dream {team_id: $profileId, dream_id: $dreamId})
    RETURN d.dream_id AS id
    `,
    { profileId: PROFILE_ID, dreamId },
  );
  expect(remainingDreams).toHaveLength(0);

  const fragments = await neo4jQuery(
    `
    MATCH (sf:SourceFragment {team_id: $profileId, source: $source})
    RETURN sf.fragment_id AS id,
           sf.content AS content,
           sf.labels AS labels,
           sf.metadata_json AS metadata_json
    `,
    {
      profileId: PROFILE_ID,
      source: `dream_feedback:${dreamId}`,
    },
  );
  expect(fragments).toHaveLength(1);
  expect(fragments[0].content).toContain(feedback);
  expect(fragments[0].labels).toEqual(
    expect.arrayContaining(['dream_feedback', 'dream_confirmed']),
  );
  const metadata = JSON.parse(fragments[0].metadata_json as string);
  expect(metadata).toMatchObject({
    dream_id: dreamId,
    dream_decision: 'confirm_true',
  });
});
