import assert from "node:assert/strict";
import { test } from "node:test";
import { parseProgressStream } from "./cases/progress.mjs";

test("progress reader reconstructs split UTF-8 and CRLF frames without losing ordering", async () => {
  const payload = 'event: message\r\ndata: {"method":"notifications/progress","params":{"message":"Assessing Evidence é"}}\r\n\r\ndata: {"id":1,"result":{}}\n\n';
  const bytes = new TextEncoder().encode(payload);
  const frames = [];
  async function* chunks() {
    for (const byte of bytes) yield Uint8Array.of(byte);
  }
  await parseProgressStream(chunks(), (frame) => frames.push(frame));
  assert.equal(frames.length, 2);
  assert.equal(frames[0].params.message, "Assessing Evidence é");
  assert.equal(frames[1].id, 1);
});

test("progress reader rejects incomplete and oversized captures", async () => {
  async function* partial() { yield new TextEncoder().encode('data: {"id":1'); }
  await assert.rejects(parseProgressStream(partial(), () => {}), /incomplete event/);
  async function* oversized() { yield new Uint8Array((4 << 20) + 1); }
  await assert.rejects(parseProgressStream(oversized(), () => {}), /bounded capture/);
});
