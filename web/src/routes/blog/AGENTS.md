# Blog posts

Post metadata lives in `postIndex.ts` (newest first; `posts.test.ts` pins the first one)
and markdown bodies live in `content/<slug>.ts` (re-exported by `posts.ts`),
published at `https://gitslice.io/blogs/<slug>`.

## Audience: users, not us

The blog is written for people who use Gitslice. Describe what a feature
does for them, how to use it, and what to expect. Do not describe how we run
or build the service:

- No hosting or infrastructure: Cloud Run, Cloudflare Workers, R2, Neon,
  Cloud Build, instances, CPU throttling, scale to zero, and so on.
- No server internals: internal endpoints and self requests, queues, leases,
  migrations, table names, cache directories, package or file paths in our
  source, and design doc numbers.
- Keep user-visible behavior: commands, settings, limits, retry schedules,
  error messages, what is pushed or stored, and security guarantees a user
  relies on (stated as guarantees, not as how they are implemented).

If a detail only makes sense to someone reading our source, it belongs in
`design/`, not in a post. Before landing, reread the post as a user would and
cut anything they can't act on.

## Style

Plain, direct sentences; numbered `##` sections; code blocks for commands.
Every fenced block names its language (`bash`, `yaml`, `go`, ...) so it is
highlighted; add `title=<file>` after the language to label a file
(`yaml title=.gitslice/automations.yaml`). A `mermaid` block is drawn as a
diagram. `renderPost.ts` renders posts on the server, so test a new post with
`npx vitest run src/routes/blog`.
Link to the docs (`/doc/...`) for reference material instead of repeating it.
Escape backticks and `${` inside the template literal.
