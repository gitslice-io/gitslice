import { PageHeader } from "../components/PageHeader";
import { OwnedAgents } from "../components/slices/OwnedAgents";
import { PendingClaims } from "../components/slices/PendingClaims";

// ClaimsPage is the stable link `gs auth register-agent` prints for the owner:
// sign in, accept the agent, and see the agents you already co-own.
export function ClaimsPage() {
  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            Agent claims
          </h1>
        }
      />
      <div className="mt-2 grid gap-8">
        <PendingClaims showEmpty />
        <OwnedAgents />
      </div>
    </section>
  );
}
