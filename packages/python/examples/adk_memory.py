import asyncio
import os

from google.adk.events import Event
from google.adk.sessions import Session
from google.genai import types

from densemem.adk import DenseMemMemoryService


def make_session() -> Session:
    return Session(
        app_name="adk-example",
        user_id="alice",
        id="retained-session-1",
        events=[
            Event(
                id="retained-user-event-1",
                author="user",
                timestamp=1792497600,
                content=types.Content(
                    role="user", parts=[types.Part(text="  Ari uses Go. café 🧭\n")]
                ),
            )
        ],
    )


async def main() -> None:
    memory = DenseMemMemoryService(
        os.environ["DENSEMEM_ENDPOINT"],
        token=os.environ["DENSEMEM_TOKEN"],
        bound_user_id="alice",
    )
    snapshot = make_session()
    await memory.add_session_to_memory(snapshot)
    response = await memory.search_memory(app_name="adk-example", user_id="alice", query="Ari Go")
    print(response.model_dump_json())


if __name__ == "__main__":
    asyncio.run(main())
