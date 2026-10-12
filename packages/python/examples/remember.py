import asyncio
import os

from densemem import DenseMemClient, RememberRequest, RememberResult


def make_request(key: str) -> RememberRequest:
    return {
        "idempotency_key": key,
        "evidence": [
            {
                "content": "Dense-Mem stores durable memory in PostgreSQL.",
                "source_type": "manual",
                "source": "Python core example",
                "metadata": {"origin": "python-example"},
            }
        ],
        "relationships": [
            {
                "ref": "durable-store",
                "subject": {"name": "Dense-Mem", "entity_kind": "project"},
                "predicate": {"proposed_key": "stores_durable_memory_in"},
                "object": {"entity": {"name": "PostgreSQL", "entity_kind": "product"}},
                "polarity": "+",
                "evidence_indices": [0],
            }
        ],
    }


async def run(client: DenseMemClient, key: str) -> RememberResult:
    request = make_request(key)
    result = await client.remember(request)
    replay = await client.remember(request)
    assert replay == result
    await client.recall_memory({"query": "Dense-Mem durable memory PostgreSQL", "limit": 5})
    for proposal in result["relationship_results"]:
        for split in proposal["splits"]:
            await client.trace_memory({"relationship_id": split["relationship_id"]})
    return result


if __name__ == "__main__":
    client = DenseMemClient(os.environ["DENSEMEM_ENDPOINT"], token=os.environ["DENSEMEM_TOKEN"])
    result = asyncio.run(run(client, os.environ["DENSEMEM_OPERATION_KEY"]))
    print(result["processing_state"])
