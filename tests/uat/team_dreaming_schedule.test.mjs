import assert from "node:assert/strict";
import test from "node:test";

import { nextScheduledUTCMinute } from "./team_dreaming_schedule.mjs";

const minute = 60_000;
const boundary = Date.UTC(2026, 7, 7, 12, 35, 0, 0);

const cases = [
  {
    name: "immediately before a minute boundary",
    now: boundary - 1,
    expected: boundary + 4 * minute,
  },
  {
    name: "exactly on a minute boundary",
    now: boundary,
    expected: boundary + 4 * minute,
  },
  {
    name: "immediately after a minute boundary",
    now: boundary + 1,
    expected: boundary + 5 * minute,
  },
];

for (const { name, now, expected } of cases) {
  test(`nextScheduledUTCMinute: ${name}`, () => {
    const target = nextScheduledUTCMinute(now);

    assert.equal(target.getTime(), expected);
    assert.equal(target.getTime() % minute, 0);
    assert.ok(target.getTime() >= now + 4 * minute);
  });
}
