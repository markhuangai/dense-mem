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
  for (const input of ['data: {"id":1', 'data: {"id":1,"result":{}}', 'data: {"id":1,"result":{}}\n', 'data: {"id":1,"result":{}}\r\n']) {
    const frames = [];
    async function* partial() { yield new TextEncoder().encode(input); }
    await assert.rejects(parseProgressStream(partial(), (frame) => frames.push(frame)), /incomplete event/);
    assert.equal(frames.length, 0);
  }
  async function* oversized() { yield new Uint8Array((4 << 20) + 1); }
  await assert.rejects(parseProgressStream(oversized(), () => {}), /bounded capture/);
});

test("progress reader waits for the event delimiter and awaits callbacks in order", async () => {
  const frames = [];
  const encode = (text) => new TextEncoder().encode(text);
  async function* delayed() {
    yield encode('data: {"method":"notifications/progress"}\r\n');
    assert.equal(frames.length, 0);
    yield encode("\r");
    assert.equal(frames.length, 0);
    yield encode("\n");
    assert.equal(frames.length, 1);
    yield encode('data:{\ndata:"id":1,"result":{}}\n: comment\n');
    assert.equal(frames.length, 1);
    yield encode("\n");
  }
  await parseProgressStream(delayed(), async (frame) => {
    await Promise.resolve();
    frames.push(frame);
  });
  assert.equal(frames.length, 2);
  assert.equal(frames[0].method, "notifications/progress");
  assert.equal(frames[1].id, 1);
});

test("progress reader retains completed events when the next event is invalid", async () => {
  for (const ending of ['data: {"id":1,"result":{}}\n', 'data: {\n\n']) {
    const frames = [];
    async function* chunks() {
      yield new TextEncoder().encode('data: {"method":"notifications/progress"}\n\n' + ending);
    }
    await assert.rejects(parseProgressStream(chunks(), (frame) => frames.push(frame)));
    assert.deepEqual(frames, [{ method: "notifications/progress" }]);
  }
});

test("progress reader bounds an event accumulated across data lines", async () => {
  const frames = [];
  async function* chunks() {
    const chunk = new TextEncoder().encode("data: " + " ".repeat(1024) + "\n");
    for (let index = 0; index < 4096; index++) yield chunk;
  }
  await assert.rejects(parseProgressStream(chunks(), (frame) => frames.push(frame)), /bounded capture/);
  assert.equal(frames.length, 0);
});
