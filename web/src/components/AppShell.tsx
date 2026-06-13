import { Outlet, useLocation } from "@tanstack/react-router";
import { useEffect } from "react";

import { useSelection } from "../state/selection";
import { Sidebar } from "./Sidebar";
import { TopBar } from "./TopBar";

export function AppShell() {
  const location = useLocation();
  const { account, ref } = useSelection();

  useEffect(() => {
    const url = new URL(window.location.href);
    if (account) {
      url.searchParams.set("account", account);
    } else {
      url.searchParams.delete("account");
    }
    if (ref) {
      url.searchParams.set("ref", ref);
    } else {
      url.searchParams.delete("ref");
    }

    const nextUrl = `${url.pathname}${url.search}${url.hash}`;
    const currentUrl = `${window.location.pathname}${window.location.search}${window.location.hash}`;
    if (nextUrl !== currentUrl) {
      window.history.replaceState(null, "", nextUrl);
    }
  }, [account, location.pathname, ref]);

  return (
    <div className="min-h-[100dvh] bg-slate-50 text-zinc-900 md:grid md:grid-cols-[15rem_minmax(0,1fr)]">
      <Sidebar />
      <div className="min-w-0">
        <TopBar />
        <main className="px-4 py-6 md:px-8 md:py-8">
          <Outlet />
        </main>
      </div>
    </div>
  );
}
