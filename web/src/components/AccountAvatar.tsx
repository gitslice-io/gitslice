import { useState, useSyncExternalStore } from "react";

import { cn } from "../lib/cn";

// Account pictures come from /avatars/{account}.png: the uploaded picture, or
// a pixel picture generated from the name. The API draws them and the site's
// Worker caches them at the edge. Adding ?v= (the profile's avatarVersion, or
// "none" for the generated one) makes the address change with the picture,
// so it can be cached for good; without it a change shows within minutes.

const knownVersions = new Map<string, string>();
const listeners = new Set<() => void>();

/**
 * Records the picture an account shows now (its profile's avatarVersion), so
 * every avatar of it on the page switches to the new address at once, e.g.
 * right after an upload.
 */
export function rememberAvatarVersion(account: string, version: string) {
  if (knownVersions.get(account) === version) {
    return;
  }
  knownVersions.set(account, version);
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void) {
  listeners.add(listener);
  return () => {
    listeners.delete(listener);
  };
}

function useKnownVersion(account: string) {
  return useSyncExternalStore(
    subscribe,
    () => knownVersions.get(account),
    () => undefined
  );
}

export function avatarUrl(account: string, version?: string) {
  const path = `/avatars/${encodeURIComponent(account)}.png`;
  return version === undefined ? path : `${path}?v=${encodeURIComponent(version || "none")}`;
}

export function AccountAvatar({
  account,
  kind,
  size = "sm",
  version
}: {
  account: string;
  kind: string;
  size?: "xs" | "sm" | "lg";
  /** The profile's avatarVersion when the caller has it. */
  version?: string;
}) {
  const known = useKnownVersion(account);
  const src = avatarUrl(account, version ?? known);
  const [failedSrc, setFailedSrc] = useState<string | null>(null);
  const shape = cn(
    "shrink-0",
    size === "lg" ? "size-12" : size === "xs" ? "size-4" : "size-6",
    // Organizations are square, people and agents round, as on GitHub.
    kind === "organization" ? "rounded-md" : "rounded-full"
  );

  if (failedSrc === src) {
    // No picture to show (say, a local server without the Worker): the
    // account's first letter, as before pictures.
    return (
      <span
        aria-hidden
        className={cn(
          shape,
          "grid place-items-center font-semibold uppercase text-white",
          size === "lg" ? "text-lg" : size === "xs" ? "text-[9px]" : "text-[11px]",
          kind === "organization" ? "bg-indigo-600" : kind === "agent" ? "bg-teal-600" : "bg-zinc-700"
        )}
      >
        {account.slice(0, 1)}
      </span>
    );
  }

  return (
    <img
      alt=""
      aria-hidden
      className={cn(shape, "bg-slate-100 object-cover dark:bg-zinc-800")}
      decoding="async"
      draggable={false}
      loading="lazy"
      onError={() => setFailedSrc(src)}
      src={src}
    />
  );
}

// Account names: lowercase letters, digits and inner dashes. An author the
// server could not name comes as a raw subject id, which has no picture.
const accountName = /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/;

export function isAccountName(name: string) {
  return accountName.test(name);
}

/**
 * An author's name with their picture in front, for bylines: changesets,
 * history, blame. Authors are people or agents, so the picture is round.
 */
export function AuthorLabel({ className, name }: { className?: string; name: string }) {
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1.5", className)}>
      {isAccountName(name) ? <AccountAvatar account={name} kind="personal" size="xs" /> : null}
      <span className="min-w-0 truncate">{name}</span>
    </span>
  );
}
