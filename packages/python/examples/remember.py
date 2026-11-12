import asyncio
import os

from densemem import DenseMemClient, RememberRequest, RememberResult


def make_request(key: str) -> RememberRequest:
    return {
        "idempotency_key": key,
        "target_space": "private",
        "evidence": [
            {
                "content": "Dense-Mem stores durable memory in PostgreSQL.",
                "source_type": "manual",
                "source": "Python core example",
                "metadata": {"origin": "python-example", "revision": "1"},
            }
        ],

    }


async def run(client: DenseMemClient, key: str) -> RememberResult:
    request = make_request(key)
    result = await client.remember(request)
    replay = await client.remember(request)
    assert replay == result
    await client.recall_memory({"query": "Dense-Mem durable memory PostgreSQL", "limit": 5})
    for item in result["evidence"]:
        await client.trace_memory({"evidence_id": item["evidence_id"]})
    return result


if __name__ == "__main__":
    client = DenseMemClient(os.environ["DENSEMEM_ENDPOINT"], token=os.environ["DENSEMEM_TOKEN"])
    result = asyncio.run(run(client, os.environ["DENSEMEM_OPERATION_KEY"]))
    print(result["processing_state"])
