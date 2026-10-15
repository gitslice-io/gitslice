import { Link } from "@tanstack/react-router";

import { PageMessage } from "./";
import { errorMessage } from "./status";

const messageLinkClass =
  "rounded-md border border-slate-300 dark:border-zinc-700 bg-white dark:bg-zinc-900 px-3 py-1.5 text-sm font-semibold text-slate-700 dark:text-zinc-300 transition hover:bg-slate-50 dark:hover:bg-zinc-950";

// Why a changeset did not load, in terms of what the viewer can do: a wrong
// id, a private slice to sign in to, or a slice they are not a member of.
export function ChangesetLoadError({
  changesetId,
  error,
  signedIn
}: {
  changesetId: string;
  error: unknown;
  signedIn: boolean;
}) {
  const status = (error as { status?: number } | null)?.status;
  if (status === 404) {
    return (
      <PageMessage
        message="Nothing matches this id, as a changeset or as a commit a changeset landed. Check the link: gs prints changeset ids as 10 hex characters, with a view: link next to them."
        title={`No changeset ${changesetId}`}
      />
    );
  }
  if (status === 401 || (status === 403 && !signedIn)) {
    return (
      <PageMessage
        message="This changeset is in a private slice. Sign in to see it if you are a member."
        title="Sign in to see this changeset"
      >
        <Link className={messageLinkClass} to="/login">
          Sign in
        </Link>
      </PageMessage>
    );
  }
  if (status === 403) {
    return (
      <PageMessage
        message="It is in a private slice you are not a member of. Ask the slice's owner to add you. If an agent of yours made it, claim the agent to see its slices."
        title="You can't see this changeset"
      >
        <Link className={messageLinkClass} to="/claims">
          Claim agents
        </Link>
      </PageMessage>
    );
  }
  return (
    <PageMessage message={errorMessage(error)} title="Unable to load changeset" />
  );
}
