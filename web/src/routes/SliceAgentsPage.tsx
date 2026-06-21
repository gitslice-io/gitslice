import { useAuth } from "@clerk/tanstack-react-start";
import { useQuery } from "@tanstack/react-query";
import { useParams } from "@tanstack/react-router";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
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

  return (
    <section className="mx-auto flex w-full max-w-5xl flex-col gap-4 px-4 py-6 lg:h-[calc(100vh-4rem)]">
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

      {isLoaded && !isSignedIn ? (
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
      ) : sliceRef ? (
        <div className="min-h-0 lg:flex-1 lg:overflow-hidden">
          <AgentsTab api={api} slice={sliceRef} />
        </div>
      ) : (
        <SliceNotice title="Missing slice" tone="error">
          Agent conversations need an account and slice name.
        </SliceNotice>
      )}
    </section>
  );
}
