import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { useNavigate, useParams } from "@tanstack/react-router";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { AgentsTab } from "../components/slices/AgentsTab";
import {
  SliceLoadingBlock,
  SliceNotice,
  getErrorMessage
} from "../components/slices/SlicePageParts";
import { toSliceRouteParams } from "../lib/sliceRoutes";

interface SliceParams {
  account?: string;
  slice?: string;
}

export function SliceAgentsPage() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const navigate = useNavigate();
  const params = useParams({ strict: false }) as SliceParams;
  const routeAccount = params.account ?? "";
  const routeSlice = params.slice ?? "";
  const routeSliceRef =
    routeAccount && routeSlice
      ? { account: routeAccount, slice: routeSlice }
      : undefined;

  const sliceQuery = useQuery({
    enabled: Boolean(isLoaded && routeSliceRef),
    queryKey: ["sliceRef", routeAccount, routeSlice],
    queryFn: () => api.resolveSlice({ ref: routeSliceRef })
  });

  const slice = sliceQuery.data;
  const sliceRef = slice?.ref ?? routeSliceRef;
  const sliceLabel = sliceRef ? `${sliceRef.account}:${sliceRef.slice}` : "";
  const sliceRouteParams = toSliceRouteParams(sliceRef);

  const breadcrumb = (
    <Breadcrumb
      items={[
        { label: "Slices", to: "/slices" },
        sliceRouteParams
          ? {
              label: sliceLabel,
              to: "/slices/$account/$slice",
              params: sliceRouteParams
            }
          : { label: sliceLabel },
        { label: "Agents" }
      ]}
    />
  );

  // PageHeader keeps the breadcrumb pinned at the top across every state — the
  // same convention as the changesets list page — instead of scrolling away
  // inside the conversation.
  const isSliceLoading = sliceQuery.isPending && Boolean(routeSliceRef);
  const showConversation =
    sliceRef &&
    !(isLoaded && !isSignedIn) &&
    !isSliceLoading &&
    !sliceQuery.isError;

  return (
    <section className="mx-auto flex h-[calc(100dvh-6.5rem)] w-full max-w-[100rem] flex-col gap-4 overflow-hidden sm:h-[calc(100dvh-7rem)] md:h-[calc(100dvh-8rem)]">
      <PageHeader
        actions={[
          ...(sliceRouteParams
            ? [
                {
                  label: "Slice detail",
                  onSelect: () =>
                    navigate({
                      to: "/slices/$account/$slice",
                      params: sliceRouteParams as never
                    })
                }
              ]
            : []),
          {
            label: "Changesets",
            onSelect: () =>
              navigate({
                to: "/changesets",
                search: { slice: sliceLabel } as never
              })
          },
          ...(sliceRouteParams
            ? [
                {
                  label: "Settings",
                  onSelect: () =>
                    navigate({
                      to: "/slices/$account/$slice/settings",
                      params: sliceRouteParams as never
                    })
                }
              ]
            : [])
        ]}
        breadcrumb={breadcrumb}
      />

      {showConversation ? (
        <div className="min-h-0 flex-1 overflow-hidden">
          <AgentsTab api={api} slice={sliceRef} />
        </div>
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
