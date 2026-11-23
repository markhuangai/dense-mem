---
workflow: "session"
order: 2
anchor: "select-exact-excerpts"
title: "Select exact excerpts"
chapter: "Extract"
figure: "Ingest a private session"
caption: "The extractor proposes ranges in the current events."
guide: "/Technical-Reference/#ingest_session"
question: "Inspect this step"
details: ["Extraction preserves the original offsets and avoids treating previous context as a new intake. The status and returned Evidence handles tell the host what happened."]
---

For each selected excerpt, the server checks the event and offsets, then slices the original characters. A session with no selected memory can complete explicitly without Evidence.
