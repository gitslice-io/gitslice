import { Link } from "@tanstack/react-router";

import type { AccountProfile } from "../../api/types";
import { AccountAvatar } from "../../components/AccountAvatar";
import { Button, buttonClassName } from "../../components/ui/Button";
import { canAdmin, kindLabel, type Membership } from "../../lib/accounts";

function article(role: string) {
  return /^[aeiou]/.test(role) ? "an" : "a";
}

export function AccountProfileHeader({
  account,
  activeAccount,
  canEditProfile,
  displayKind,
  editing,
  membership,
  onEdit,
  onSwitchAccount,
  profile
}: {
  account: string;
  activeAccount: string | null | undefined;
  canEditProfile: boolean;
  displayKind: string;
  editing: boolean;
  membership: Membership | undefined;
  onEdit(): void;
  onSwitchAccount(account: string): void;
  profile: AccountProfile | undefined;
}) {
  return (
    <div className="mt-4 flex flex-wrap items-center gap-x-4 gap-y-3">
      <AccountAvatar
        account={account}
        kind={displayKind}
        size="xl"
        version={profile ? (profile.avatarVersion ?? "") : undefined}
      />
      {/* At least this wide, so the buttons wrap below it on a phone
          instead of squeezing the name into a sliver. */}
      <div className="min-w-[12rem] flex-1">
        <h1 className="truncate text-xl font-semibold text-zinc-950 dark:text-zinc-50">
          {profile?.displayName || account}
        </h1>
        <p className="text-sm text-slate-600 dark:text-zinc-400">
          {profile?.displayName ? `@${account} · ` : ""}
          {displayKind ? kindLabel(displayKind) : "Account"}
          {membership?.role ? ` · you are ${article(membership.role)} ${membership.role}` : ""}
        </p>
      </div>
      <div className="flex basis-full flex-wrap items-center gap-2 sm:basis-auto">
        {canEditProfile && !editing ? (
          <Button onClick={onEdit} size="md" variant="secondary">
            Edit profile
          </Button>
        ) : null}
        {membership && membership.account !== activeAccount ? (
          <Button onClick={() => onSwitchAccount(membership.account)} size="md" variant="secondary">
            Switch to {account}
          </Button>
        ) : null}
        {canAdmin(membership) ? (
          <Link
            className={buttonClassName({ size: "md", variant: "secondary" })}
            params={{ account }}
            to="/accounts/$account/secrets"
          >
            Secrets
          </Link>
        ) : null}
        {canAdmin(membership) ? (
          <Link
            className={buttonClassName({ size: "md", variant: "primary" })}
            search={{ account }}
            to="/slices/new"
          >
            New slice
          </Link>
        ) : null}
      </div>
    </div>
  );
}
