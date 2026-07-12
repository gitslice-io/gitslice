import { useNavigate } from "@tanstack/react-router";
import { useState } from "react";

import type { Conversation } from "../api/types";
import { PageHeader } from "../components/PageHeader";
import { NewConversationDialog } from "../components/slices/NewConversationDialog";
import { RecentConversations } from "../components/slices/RecentConversations";
import { SlicesList } from "../components/slices/SlicesList";
import { toSliceRouteParams } from "../lib/sliceRoutes";

export function HomePage() {
  const navigate = useNavigate();
  const [isCreateOpen, setIsCreateOpen] = useState(false);

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
            Home
          </h1>
        }
      />
      <NewConversationDialog
        onClose={() => setIsCreateOpen(false)}
        onCreated={navigateToConversation}
        open={isCreateOpen}
      />
      <div className="mt-2 grid gap-8">
        <RecentConversations />
        <SlicesList />
      </div>
    </section>
  );
}
