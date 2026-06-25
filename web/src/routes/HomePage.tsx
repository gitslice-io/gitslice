import { Link } from "@tanstack/react-router";

import { PageHeader } from "../components/PageHeader";
import { RecentConversations } from "../components/slices/RecentConversations";
import { SlicesList } from "../components/slices/SlicesList";

export function HomePage() {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        primaryAction={
          <Link
            className="rounded-md bg-zinc-950 px-4 py-2 text-sm font-semibold text-white transition hover:bg-zinc-800 active:scale-[0.98]"
            to="/slices/new"
          >
            New slice
          </Link>
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 sm:text-lg">
            Home
          </h1>
        }
      />
      <div className="mt-2 grid gap-8">
        <RecentConversations />
        <SlicesList />
      </div>
    </section>
  );
}
