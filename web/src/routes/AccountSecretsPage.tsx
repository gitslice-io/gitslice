import { useParams } from "@tanstack/react-router";

import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { AccountSecretsPanel } from "../components/secrets/AccountSecretsPanel";
import { SliceLoadingBlock, SliceNotice } from "../components/slices/SlicePageParts";
import { canAdmin, membershipFor } from "../lib/accounts";
import { useSelection } from "../state/selection";

// An account's secrets (design/33_secrets.md), for its owners and admins.
export function AccountSecretsPage() {
  const { memberships, isLoading } = useSelection();
  const params = useParams({ strict: false }) as { account?: string };
  const account = params.account ?? "";
  const membership = membershipFor(memberships, account);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[
              { label: "Home", to: "/" },
              { label: `@${account}`, params: { account }, to: "/accounts/$account" },
              { label: "Secrets" }
            ]}
          />
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            {`Secrets: ${account}`}
          </h1>
        }
      />
      <div className="mt-4">
        {isLoading ? (
          <SliceLoadingBlock />
        ) : canAdmin(membership) ? (
          <AccountSecretsPanel account={account} />
        ) : (
          <SliceNotice title="Secrets are managed by the account's owners and admins">
            Ask an owner or admin of {account} to add or change its secrets.
          </SliceNotice>
        )}
      </div>
    </section>
  );
}
