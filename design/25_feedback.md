# Feedback

Agents run into Gitslice's rough edges first, and they say so in their
transcripts, where the Gitslice team never sees it. `gs feedback` gives anyone
signed in, agent or person, one command to tell the team, and gives the team
one place to read it, answer it, and let the sender read the answer.

## Sending

```bash
gs feedback "gs sync ran silent for 440 paths; show progress" --category idea
gs feedback send --stdin --category bug < notes.md
gs feedback "..." --context command="gs fs upload ./site /me/site --recursive"
```

- `gs feedback [message]` and `gs feedback send [message]` are the same.
  `--stdin` reads the message instead.
- `--category` is `bug`, `idea`, `docs` or `other` (the default).
- gs adds context: `gs_version`, `os`, `arch`, and `workspace` (account/slice)
  when run in one. `--context key=value` adds more (repeatable).
- It prints the feedback id. `--json` returns the stored feedback.

Limits, enforced by the server: the message is 1 to 20,000 characters; at most
20 context entries, keys up to 64 characters and values up to 1,000; and 30
pieces of feedback per sender per rolling 24 hours (`ResourceExhausted`).
Sending needs a signed-in subject; there is no anonymous feedback.

## Reading and answering

```bash
gs feedback list --mine                 # yours, every status, with answers
gs feedback list                        # the team: open feedback, newest first
gs feedback list --status all --json    # the team: everything, for scripts
gs feedback resolve <id> --note "progress lines ship in v0.4.4"
gs feedback resolve <id> --reopen
```

The team is the server's operators (`GITSLICE_OPERATOR_SUBJECTS`) and the
owners and admins of the feedback account (`GITSLICE_FEEDBACK_ACCOUNT`,
default `gitslice`; `none` leaves only the operators). Anyone else listing
without `--mine` gets `PermissionDenied`, and the CLI points them to `--mine`.

Resolving requires a note: it is the answer the sender reads in
`gs feedback list --mine`, so it should say what changed and where (a release,
a changeset link), or why nothing will. `--reopen` sets the status back to
open and keeps the earlier answer. Lists are newest first and page with
`--limit` (up to 200) and `--cursor`.

## API

`FeedbackService` in `proto/core/v1/feedback.proto`:

- `SubmitFeedback(body, category, context) → Feedback`
- `ListFeedback(status, mine, limit, cursor) → {feedback[], next_cursor}`
- `ResolveFeedback(id, resolution, reopen) → Feedback`

`Feedback` carries `id` (`fb_…`), `subject_id`, `account` (the sender's own
account at the time), `body`, `category`, `context`, `status` (`open` or
`resolved`), `resolution`, `resolved_by` (the answerer's username),
`resolved_at` and `created_at` (RFC 3339).

Storage is the `feedback` table (migration 0033), indexed for newest-first
listing overall, by status, and by sender. Feedback is kept; nothing prunes it.

## Not yet

- A web page for the team's queue. The CLI's `--json` output is the interface
  for now.
- Notifying the team when feedback arrives (a webhook event, or email).
- Letting the team reply without resolving, or the sender add to a thread.
