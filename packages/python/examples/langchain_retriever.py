import asyncio
import os

from densemem.langchain import DenseMemRetriever


async def main() -> None:
    retriever = DenseMemRetriever(
        endpoint=os.environ["DENSEMEM_ENDPOINT"],
        token=os.environ["DENSEMEM_TOKEN"],
        k=10,
    )
    documents = await retriever.ainvoke("Which database does this project use?")
    for document in documents:
        print(document.page_content, document.metadata["densemem"]["evidence_id"])


if __name__ == "__main__":
    asyncio.run(main())
