import { useAuth } from "@clerk/tanstack-react-start";
import { Link, useRouterState } from "@tanstack/react-router";
import { useState } from "react";

import { dismissalCookie, visitorBannerHiddenOn } from "../lib/visitorBanner";

// A strip under the top bar for someone who arrived by a link and is not
// signed in: what Gitslice is, and where to go from here. `show` is decided
// from cookies by the route (see lib/visitorBanner), so it is in the server's
// HTML or not from the start.
export function VisitorBanner({ show }: { show: boolean }) {
  const { isLoaded, isSignedIn } = useAuth();
  const pathname = useRouterState({ select: (state) => state.location.pathname });
  const [dismissed, setDismissed] = useState(false);

  if (!show || dismissed || (isLoaded && isSignedIn) || visitorBannerHiddenOn(pathname)) {
    return null;
  }

  return (
    <div
      aria-label="About Gitslice"
      className="border-b border-sky-200 bg-sky-50 px-3 py-2 text-sm text-sky-950 dark:border-sky-900/60 dark:bg-sky-950/40 dark:text-sky-100 sm:px-4 md:px-6"
      role="region"
    >
      <div className="mx-auto flex w-full max-w-[100rem] flex-wrap items-center gap-x-4 gap-y-2">
        <p className="min-w-0 flex-1 basis-60 leading-5">
          <span className="font-semibold">Gitslice</span> is source control for fleets of agents and the people who
          work with them.
        </p>
        <div className="flex shrink-0 items-center gap-2">
          <Link
            className="inline-flex min-h-8 items-center rounded-md px-2 text-sm font-medium text-sky-800 underline-offset-2 hover:underline dark:text-sky-200"
            to="/"
          >
            Learn more
          </Link>
          <Link
            className="inline-flex min-h-8 items-center rounded-md bg-zinc-950 px-3 text-sm font-semibold text-white transition hover:bg-zinc-800 dark:bg-zinc-100 dark:text-zinc-950 dark:hover:bg-white"
            to="/signup"
          >
            Sign up
          </Link>
          <button
            aria-label="Dismiss"
            className="inline-flex size-8 items-center justify-center rounded-md text-base leading-none text-sky-800 transition hover:bg-sky-100 dark:text-sky-200 dark:hover:bg-sky-900/50"
            onClick={() => {
              // Remembered for thirty days, and seen by the server render.
              document.cookie = dismissalCookie(window.location.protocol === "https:");
              setDismissed(true);
            }}
            type="button"
          >
            ×
          </button>
        </div>
      </div>
    </div>
  );
}
