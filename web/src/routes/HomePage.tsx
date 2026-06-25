import { PageHeader } from "../components/PageHeader";
import { RecentConversations } from "../components/slices/RecentConversations";
import { SlicesList } from "../components/slices/SlicesList";

export function HomePage() {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
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
