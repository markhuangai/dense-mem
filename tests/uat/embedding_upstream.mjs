const required = (name) => {
  const value = process.env[name];
  if (!value) throw new Error(`missing ${name}`);
  return value;
};

export async function forwardEmbedding(payload, response) {
  let baseURL, apiKey, model, dimensions, maxBatchItems;
  try {
    baseURL = required("DENSE_MEM_E2E_EMBEDDING_BASE_URL").replace(/\/$/, "");
    apiKey = required("DENSE_MEM_E2E_EMBEDDING_API_KEY");
    model = required("DENSE_MEM_E2E_EMBEDDING_MODEL");
    dimensions = Number(required("DENSE_MEM_E2E_EMBEDDING_DIMENSIONS"));
    maxBatchItems = Number(required("DENSE_MEM_E2E_EMBEDDING_MAX_BATCH_ITEMS"));
  } catch {
    return { status: 503, body: { error: { message: "embedding upstream is not configured" } } };
  }
  const input = payload?.input;
  if (!Number.isSafeInteger(dimensions) || dimensions < 1 ||
      !Number.isSafeInteger(maxBatchItems) || maxBatchItems < 1 ||
      payload?.model !== model || payload?.dimensions !== dimensions ||
      !Array.isArray(input) || input.length < 1 || input.length > maxBatchItems ||
      input.some((item) => typeof item !== "string")) {
    return { status: 400, body: { error: { message: "invalid embedding fixture request" } } };
  }

  const controller = new AbortController();
  const abort = () => controller.abort();
  response.once("close", abort);
  try {
    const upstream = await fetch(`${baseURL}/embeddings`, {
      method: "POST",
      headers: { authorization: `Bearer ${apiKey}`, "content-type": "application/json" },
      body: JSON.stringify({ model, input }),
      signal: AbortSignal.any([controller.signal, AbortSignal.timeout(60_000)]),
    });
    if (!upstream.ok) {
      return { status: upstream.status, body: { error: { message: `embedding upstream returned status ${upstream.status}` } } };
    }
    return { status: 200, body: await upstream.json() };
  } catch {
    return { status: 502, body: { error: { message: "embedding upstream unavailable" } } };
  } finally {
    response.off("close", abort);
  }
}
