import asyncio
import os

from densemem import DenseMemClient, SessionRequest, SessionResult


def make_request(key: str) -> SessionRequest:
    return {
        "idempotency_key": key,
        "framework": "generic",
        "app_name": "python-example",
        "user_id": "external-user-1",
        "session_id": "session-1",
        "events": [
            {
                "event_id": "user-event-1",
                "text": "  Ari uses Go. café 🧭\n",
                "occurred_at": "2026-10-11T12:00:00Z",
            }
        ],
    }


async def run(client: DenseMemClient, key: str) -> SessionResult:
    request = make_request(key)
    result = await client.ingest_session(request)
    assert await client.ingest_session(request) == result
    return result


if __name__ == "__main__":
    client = DenseMemClient(os.environ["DENSEMEM_ENDPOINT"], token=os.environ["DENSEMEM_TOKEN"])
    result = asyncio.run(run(client, os.environ["DENSEMEM_OPERATION_KEY"]))
    print(result["processing_state"])
