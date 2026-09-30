import { Link } from "@tanstack/react-router";

import { RpcError } from "../../api/errors";
import { currentPath, rememberReturnTo } from "../../auth/returnTo";
import { SliceNotice, getErrorMessage } from "./SlicePageParts";

// Slice lookups fail with 401 (signed out, slice is private), 403 (signed in
// without access), or 404 (no such slice, or hidden from you). A bare "not
// found" sends people debugging the URL, so each case says what it most
// likely means and what to do next.
export function SliceAccessNotice({
  error,
  isSignedIn,
  sliceKey
}: {
  error: unknown;
  isSignedIn: boolean;
  sliceKey: string;
}) {
  const status = error instanceof RpcError ? error.status : 0;

  if (!isSignedIn && (status === 401 || status === 403 || status === 404)) {
    return (
      <SliceNotice title="This slice is private">
        <p>
          {sliceKey || "This slice"} is only visible to members of its account.
          Sign in to view it.
        </p>
        <SignInButton />
      </SliceNotice>
    );
  }

  if (status === 401) {
    return (
      <SliceNotice title="Your session expired">
        <p>Sign in again to view {sliceKey || "this slice"}.</p>
        <SignInButton />
      </SliceNotice>
    );
  }

  if (status === 403 || status === 404) {
    return (
      <SliceNotice title="No access to this slice">
        <p>
          {sliceKey || "This slice"} does not exist, or it is private and your
          account is not a member. Private slices are visible only to members
          of their account.
        </p>
        <p className="mt-2">
          If an agent created it with your email, accept the agent on your{" "}
          <Link className="font-medium underline underline-offset-2" to="/claims">
            claims page
          </Link>{" "}
          to become a co-owner.
        </p>
      </SliceNotice>
    );
  }

  return (
    <SliceNotice title="Could not load slice" tone="error">
      {getErrorMessage(error)}
    </SliceNotice>
  );
}

function SignInButton() {
  return (
    <Link
      className="mt-3 inline-flex rounded-md bg-zinc-950 px-3 py-1.5 text-xs font-semibold text-white transition hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
      onClick={() => rememberReturnTo(currentPath())}
      to="/login"
    >
      Sign in
    </Link>
  );
}
