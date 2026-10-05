import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import type { Conversation } from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { NewConversationDialog } from "../components/slices/NewConversationDialog";
import { OwnedAgents } from "../components/slices/OwnedAgents";
import { PendingClaims } from "../components/slices/PendingClaims";
import { PendingInvitations } from "../components/slices/PendingInvitations";
import { RecentConversations } from "../components/slices/RecentConversations";
import { SlicesList } from "../components/slices/SlicesList";
import { kindLabel } from "../lib/accounts";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { useSelection } from "../state/selection";

export function HomePage() {
  const navigate = useNavigate();
  const [isCreateOpen, setIsCreateOpen] = useState(false);
  const { activeAccount, activeMembership, memberships } = useSelection();
  // Home shows the account picked in the top bar. Your personal context also
  // covers the agents you own; an organization's shows only the organization.
  const personal = (activeMembership?.kind ?? "personal") === "personal";
  const contextAccounts = personal
    ? memberships.filter((m) => m.kind === "personal" || m.kind === "agent").map((m) => m.account)
    : [activeAccount];

  function navigateToConversation(conversation: Conversation) {
    const routeParams = toSliceRouteParams(conversation.slice);
    const conversationId = conversation.id;
    if (!routeParams || !conversationId) {
      return;
    }

    void navigate({
      to: "/slices/$account/$slice/agents/$conversationId",
      params: { ...routeParams, conversationId }
    });
  }

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        primaryAction={
          <button
            className="rounded-md bg-zinc-950 dark:bg-zinc-100 px-3 py-2 text-sm font-semibold text-white dark:text-zinc-950 transition hover:bg-zinc-800 dark:hover:bg-white active:scale-[0.98]"
            onClick={() => setIsCreateOpen(true)}
            type="button"
          >
            New conversation
          </button>
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            {personal || !activeAccount ? "Home" : `${activeAccount} · ${kindLabel(activeMembership?.kind ?? "")}`}
          </h1>
        }
      />
      <NewConversationDialog
        onClose={() => setIsCreateOpen(false)}
        onCreated={navigateToConversation}
        open={isCreateOpen}
      />
      <div className="mt-2 grid gap-8">
        <PendingInvitations />
        {personal ? <PendingClaims /> : null}
        <RecentConversations accounts={contextAccounts.length > 0 ? contextAccounts : undefined} />
        {personal ? <OwnedAgents /> : null}
        <SlicesList />
      </div>
    </section>
  );
}
