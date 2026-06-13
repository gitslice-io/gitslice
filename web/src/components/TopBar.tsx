import { UserButton } from "@clerk/clerk-react";

import { useSelection } from "../state/selection";

export function TopBar() {
  const { account, setAccount } = useSelection();

  return (
    <header className="flex min-h-16 items-center justify-between gap-4 border-b border-slate-200 bg-white px-4 md:px-6">
      <div className="min-w-0 flex-1">
        <label className="grid max-w-sm gap-1 text-xs font-semibold text-slate-500">
          Account
          <input
            className="h-9 rounded-md border border-slate-300 bg-white px-3 text-sm font-medium text-zinc-900 outline-none transition focus:border-slate-500"
            onChange={(event) => setAccount(event.target.value)}
            placeholder="account"
            spellCheck={false}
            value={account}
          />
        </label>
      </div>
      <div className="shrink-0">
        <UserButton afterSignOutUrl="/login" />
      </div>
    </header>
  );
}
