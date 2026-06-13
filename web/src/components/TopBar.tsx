import { UserButton } from "@clerk/clerk-react";

import { useSelection } from "../state/selection";

export function TopBar() {
  const { account } = useSelection();

  return (
    <header className="flex min-h-16 items-center justify-between gap-4 border-b border-slate-200 bg-white px-4 md:px-6">
      <div className="min-w-0 flex-1">
        {account ? (
          <div className="text-xs font-semibold text-slate-500">
            Account
            <div className="truncate text-sm font-medium text-zinc-900">{account}</div>
          </div>
        ) : null}
      </div>
      <div className="shrink-0">
        <UserButton afterSignOutUrl="/login" />
      </div>
    </header>
  );
}
