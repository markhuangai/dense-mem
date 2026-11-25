# Topics and Community placement

Team managers inspect Topics and their publication history in the team portal.
Saving requires the write grant. Cookie-authenticated writes require the portal
CSRF token. MCP remains the external memory contract.

Select a Topic or create a definition. Change its label, description, aliases,
or optional parent. Add the changes, enter a reason, and select **Save**. The
portal sends a bounded edit list to `POST /ui/api/topics/edits` with an operation
key and each record's current edit version. An active parent can be outside
the catalog page. The server checks the versions, parent scope, and hierarchy
before saving the revisions, history, audit and replay in one transaction.

A version conflict returns 409. Refresh the record, review the current values,
and apply the changes again. An unchanged operation-key retry returns the
committed result. A changed body needs a new key. The original owner can read
older drafts in History. After authorization, the legacy write routes replay
already committed requests and return 410 for new commands.

## Topic assignment and Community placement

Remember publishes validated direct memberships in its Evidence transaction.
An independent assignment can commit while another Evidence write is running.
Automatic placement uses the version of the child and optional parent. The
parent contributes to browsing without expanding Recall. A Topic filter selects
the union of direct, eligible memberships. Private Evidence is ineligible for
shared organization.

Pausing maintenance stops background work while foreground assignment continues.
Changing organization to disabled takes effect at the next settings read.

## Operator diagnosis and upgrade

At TRACE, protected stdout records manager outcomes and diagnostic payload
chunks. Join the correlation, operation, team and profile IDs, check the chunk
count, and verify the digest before reading the captured values. A stale Save
records the expected and observed version. Authorization rejection records the
outcome without the request body.

Stop all application services and verify a restorable backup before upgrading.
Migrations support fresh installs and databases from v2.8.0 onward. Bring
older populated databases through released v2.8.0 first. The relational copy
resumes from persisted batches and verifies identities, counts and fingerprints
before releasing the write fence. Recover from backup before reopening writes.
Use forward repair once the application accepts writes.
