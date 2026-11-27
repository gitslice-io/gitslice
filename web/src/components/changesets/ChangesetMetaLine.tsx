import type { ActorAttribution, Changeset } from "../../api/types";
import { AccountLink, AuthorLabel } from "../AccountAvatar";

export function effectiveChangesetAttribution(changeset: Changeset): ActorAttribution | undefined {
  const base = changeset.attribution;
  const patchsets = changeset.patchsets ?? [];
  const currentPs =
    patchsets.find((ps) => ps.id && ps.id === changeset.currentPatchsetId) ??
    patchsets[patchsets.length - 1];
  const psAttr = currentPs?.attribution;

  if (!base || !base.actingSubjectId) {
    return psAttr ?? base;
  }
  if (!psAttr || psAttr.actingSubjectId !== base.actingSubjectId) {
    return base;
  }
  return {
    ...base,
    daemonId: base.daemonId || psAttr.daemonId,
    daemonName: base.daemonName || psAttr.daemonName,
    conversationId: base.conversationId || psAttr.conversationId
  };
}

export function formatActorAttribution(
  attr?: ActorAttribution,
  fallbackAuthor?: string
): string {
  const acting = (attr?.actingUsername || fallbackAuthor || attr?.actingSubjectId || "").trim();
  if (!acting) {
    return "";
  }

  let out = acting;
  const rawCtrl =
    (attr?.controllerUsername || "").trim() ||
    (attr?.controllerSubjectId && attr.controllerSubjectId !== attr.actingSubjectId
      ? attr.controllerSubjectId.trim()
      : "");
  if (
    rawCtrl &&
    rawCtrl !== acting &&
    (!attr?.controllerSubjectId || attr.controllerSubjectId !== attr.actingSubjectId)
  ) {
    out += ` (for ${rawCtrl})`;
  }

  const daemon = (attr?.daemonName || attr?.daemonId || "").trim();
  if (daemon) {
    out += ` via ${daemon}`;
  }

  const conv = (attr?.conversationId || "").trim();
  if (conv) {
    out += `, conversation ${conv}`;
  }
  return out;
}

export function ChangesetMetaLine({ changeset }: { changeset: Changeset }) {
  const attr = effectiveChangesetAttribution(changeset);
  const acting = (attr?.actingUsername || changeset.author || attr?.actingSubjectId || "").trim();
  if (!acting) {
    return null;
  }

  const controller =
    (attr?.controllerUsername || "").trim() ||
    (attr?.controllerSubjectId && attr.controllerSubjectId !== attr.actingSubjectId
      ? attr.controllerSubjectId.trim()
      : "");
  const showController = Boolean(
    controller &&
      controller !== acting &&
      (!attr?.controllerSubjectId || attr.controllerSubjectId !== attr.actingSubjectId)
  );
  const daemon = (attr?.daemonName || attr?.daemonId || "").trim();
  const conversationId = (attr?.conversationId || "").trim();
  const hasDetails = showController || Boolean(daemon) || Boolean(conversationId);

  if (!hasDetails) {
    return (
      <span className="inline-flex min-w-0 items-center gap-1 text-xs text-slate-500 dark:text-zinc-400">
        <span className="shrink-0">by</span>
        <AuthorLabel
          className="font-medium text-slate-700 transition hover:text-zinc-950 dark:text-zinc-300 dark:hover:text-zinc-50"
          name={acting}
        />
      </span>
    );
  }

  return (
    <span className="inline-flex min-w-0 flex-wrap items-center gap-1 text-xs text-slate-500 dark:text-zinc-400">
      <span className="shrink-0">by</span>{" "}
      <AuthorLabel
        className="font-medium text-slate-700 transition hover:text-zinc-950 dark:text-zinc-300 dark:hover:text-zinc-50"
        name={acting}
      />
      {showController ? (
        <>
          {" "}
          <span className="shrink-0">
            (for{" "}
            <AccountLink
              account={controller}
              className="font-medium text-slate-700 transition hover:text-zinc-950 dark:text-zinc-300 dark:hover:text-zinc-50"
            >
              {controller}
            </AccountLink>
            )
          </span>
        </>
      ) : null}
      {daemon ? (
        <>
          {" "}
          <span className="shrink-0">
            via <span className="font-medium text-slate-700 dark:text-zinc-300">{daemon}</span>
            {conversationId ? `, conversation ${conversationId}` : ""}
          </span>
        </>
      ) : conversationId ? (
        <span className="shrink-0">{`, conversation ${conversationId}`}</span>
      ) : null}
    </span>
  );
}
