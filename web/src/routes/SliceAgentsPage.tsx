import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";
import { useCallback } from "react";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { SliceTabs } from "../components/SliceTabs";
import { AgentConversation } from "../components/slices/AgentConversation";
import { AgentsTab } from "../components/slices/AgentsTab";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePanel,
  getErrorMessage,
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";

interface SliceParams {
  account?: string;
  slice?: string;
  conversationId?: string;
}

export function SliceAgentsPage() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as SliceParams;
  const routeAccount = params.account ?? "";
  const routeSlice = params.slice ?? "";
  const conversationId = params.conversationId ?? "";
  const routeSliceRef =
    routeAccount && routeSlice
      ? { account: routeAccount, slice: routeSlice }
      : undefined;

  const sliceQuery = useQuery({
    enabled: Boolean(isLoaded && isSignedIn && routeSliceRef),
    queryKey: ["sliceRef", routeAccount, routeSlice],
    queryFn: () => api.resolveSlice({ ref: routeSliceRef }),
  });

  const conversationQuery = useQuery({
    enabled: Boolean(isLoaded && conversationId),
    queryKey: ["conversation", conversationId],
    queryFn: () => api.getConversation({ conversationId }),
  });

  const slice = sliceQuery.data;
  const sliceRef = slice?.ref ?? routeSliceRef;
  const sliceLabel = sliceRef ? `${sliceRef.account}:${sliceRef.slice}` : "";
  const sliceRouteParams = toSliceRouteParams(sliceRef);
  const breadcrumbItems = [
    { label: "Home", to: "/" },
    sliceRouteParams
      ? {
          label: sliceLabel,
          to: "/slices/$account/$slice",
          params: sliceRouteParams,
        }
      : { label: sliceLabel },
  ];
  if (conversationId) {
    const conversationTitle = conversationQuery.data?.title?.trim();
    breadcrumbItems.push({ label: conversationTitle || "Conversation" });
  }

  const onSelectConversation = useCallback(
    (id: string) => {
      void navigate({
        to: "/slices/$account/$slice/agents/$conversationId",
        params: {
          account: routeAccount,
          slice: routeSlice,
          conversationId: id,
        },
        replace: true,
      });
    },
    [navigate, routeAccount, routeSlice],
  );

  const onBackToConversations = useCallback(() => {
    void navigate({
      to: "/slices/$account/$slice/agents",
      params: { account: routeAccount, slice: routeSlice },
      replace: true,
    });
  }, [navigate, routeAccount, routeSlice]);

  const breadcrumb = <Breadcrumb items={breadcrumbItems} />;

  // The page scrolls as one document — the same model as the changesets list:
  // the app header and the contextual PageHeader scroll away, and the
  // conversation composer pins to the viewport bottom.
  const isSliceLoading = sliceQuery.isPending && Boolean(routeSliceRef);
  const showSignedInConversation =
    isLoaded &&
    isSignedIn &&
    sliceRef &&
    !isSliceLoading &&
    !sliceQuery.isError;
  const showReadOnlyConversation =
    isLoaded && !isSignedIn && Boolean(conversationId);

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={breadcrumb}
        tabs={
          sliceRouteParams ? (
            <SliceTabs
              active="conversations"
              params={sliceRouteParams}
              sliceLabel={sliceLabel}
            />
          ) : undefined
        }
      />

      {showSignedInConversation ? (
        <AgentsTab
          api={api}
          conversationId={conversationId}
          onBack={onBackToConversations}
          onSelectConversation={onSelectConversation}
          slice={sliceRef}
        />
      ) : showReadOnlyConversation ? (
        conversationQuery.isPending ? (
          <SliceLoadingBlock />
        ) : conversationQuery.isError ? (
          <SliceNotice title="Could not load conversation" tone="error">
            {getErrorMessage(conversationQuery.error)}
          </SliceNotice>
        ) : (
          <SlicePanel className="p-0">
            <AgentConversation
              api={api}
              conversation={conversationQuery.data}
              conversationId={conversationId}
              readOnly
            />
          </SlicePanel>
        )
      ) : isLoaded && !isSignedIn ? (
        <SliceNotice title="Sign in to use agents">
          Agent conversations require a signed-in account. Sign in to start or
          view conversations for this slice.
        </SliceNotice>
      ) : sliceQuery.isPending && Boolean(routeSliceRef) ? (
        <SliceLoadingBlock />
      ) : sliceQuery.isError ? (
        <SliceNotice title="Could not load slice" tone="error">
          {getErrorMessage(sliceQuery.error)}
        </SliceNotice>
      ) : (
        <SliceNotice title="Missing slice" tone="error">
          Agent conversations need an account and slice name.
        </SliceNotice>
      )}
    </section>
  );
}
