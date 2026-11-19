import { useQuery } from "@tanstack/react-query";
import { Link, useParams } from "@tanstack/react-router";

import { useApi } from "../api/useApi";
import { Breadcrumb } from "../components/Breadcrumb";
import { PageHeader } from "../components/PageHeader";
import { SliceCIDaemonPanel } from "../components/slices/SliceCIDaemonPanel";
import { SliceDefinitionSettingsPanel } from "../components/slices/SliceDefinitionSettingsPanel";
import { SliceDescriptionPanel } from "../components/slices/SliceDescriptionPanel";
import { SliceGitMirrorPanel } from "../components/slices/SliceGitMirrorPanel";
import { SliceMembersPanel } from "../components/slices/SliceMembersPanel";
import { SliceMergeOnSubmitPanel } from "../components/slices/SliceMergeOnSubmitPanel";
import {
  SliceLoadingBlock,
  SliceNotice,
  SlicePageHeader,
  SlicePanel,
  getErrorMessage,
  sliceDisplayName
} from "../components/slices/SlicePageParts";
import { useSlicePermissions } from "../lib/permissions";
import { sliceBreadcrumbItems, toSliceRouteParams } from "../lib/sliceRoutes";
import { sliceRefQuery } from "../queries/slices";
import { useSelection } from "../state/selection";

export function SliceSettingsPage() {
  const api = useApi();
  const { accounts: viewerAccounts } = useSelection();
  const params = useParams({ from: "/app/slices/$account/$slice/settings" });
  const routeAccount = params.account ?? "";
  const routeSlice = params.slice ?? "";
  const routeSliceRef =
    routeAccount && routeSlice
      ? { account: routeAccount, slice: routeSlice }
      : undefined;

  const sliceQuery = useQuery({
    ...sliceRefQuery(api, routeAccount, routeSlice),
    enabled: Boolean(routeSliceRef)
  });

  const slice = sliceQuery.data;
  const slicePermissions = useSlicePermissions(slice ?? routeSliceRef);
  const sliceRouteParams = toSliceRouteParams(slice?.ref ?? routeSliceRef);
  const sliceRouteKey = sliceRouteParams
    ? `${sliceRouteParams.account}:${sliceRouteParams.slice}`
    : `${routeAccount}:${routeSlice}`;

  if (sliceQuery.isLoading) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SliceLoadingBlock />
      </section>
    );
  }

  if (sliceQuery.isError) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SlicePageHeader title="Slice Settings" />
        <div className="mt-8">
          <SliceNotice title="Could not load slice" tone="error">
            {getErrorMessage(sliceQuery.error)}
          </SliceNotice>
        </div>
      </section>
    );
  }

  if (!slice) {
    return (
      <section className="mx-auto w-full max-w-[100rem]">
        <SlicePageHeader title="Slice Settings" />
        <div className="mt-8">
          <SliceNotice title="Slice not found">
            No slice was returned for {sliceRouteKey || "unknown"}.
          </SliceNotice>
        </div>
      </section>
    );
  }

  const sliceLabel = sliceDisplayName(slice);
  const sliceCrumbs = sliceBreadcrumbItems(slice.ref ?? routeSliceRef, viewerAccounts);
  // Slice admins (including external or @group slice admins) manage slice
  // members; account owners and admins manage the slice definition, CI daemon,
  // secrets, and Git mirror.
  const settingsSlice = slice.ref ?? routeSliceRef;
  const canManageMembers = Boolean(settingsSlice) && slicePermissions.canAdmin;
  const canManageAccountSettings = Boolean(settingsSlice) && slicePermissions.isAccountAdmin;

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <PageHeader
        breadcrumb={
          <Breadcrumb
            items={[
              { label: "Home", to: "/" },
              ...(sliceCrumbs.length > 0 ? sliceCrumbs : [{ label: sliceLabel }]),
              { label: "Settings" }
            ]}
          />
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 dark:text-zinc-50 sm:text-lg">
            {`Slice Settings: ${sliceLabel}`}
          </h1>
        }
      />
      <p className="mb-4 text-sm leading-6 text-slate-600 dark:text-zinc-400">
        {canManageAccountSettings
          ? `A slice is a named view over folders in /${routeAccount}. Configure included paths, visibility, access, merge behavior, and Git mirrors here.`
          : "Manage access and inspect configuration for this slice view."}
      </p>

      {canManageAccountSettings ? (
        <div className="mt-8">
          <SliceDescriptionPanel slice={slice} />
        </div>
      ) : null}

      {canManageAccountSettings ? (
        <div className="mt-8">
          <SliceMergeOnSubmitPanel slice={slice} />
        </div>
      ) : null}

      <SliceDefinitionSettingsPanel
        canEdit={canManageAccountSettings}
        isReloading={sliceQuery.isFetching}
        onReload={() => {
          void sliceQuery.refetch();
        }}
        routeAccount={routeAccount}
        routeSlice={routeSlice}
        slice={slice}
      >
        <SliceCIDaemonPanel
          canEdit={canManageAccountSettings}
          routeAccount={routeAccount}
          routeSlice={routeSlice}
          slice={slice}
        />
      </SliceDefinitionSettingsPanel>

      {canManageMembers && settingsSlice ? (
        <div className="mt-8">
          <SliceMembersPanel slice={settingsSlice} />
        </div>
      ) : null}

      {canManageAccountSettings && settingsSlice ? (
        <div className="mt-8">
          <SlicePanel>
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="min-w-0 max-w-2xl">
                <h2 className="text-sm font-semibold text-zinc-950 dark:text-zinc-50">
                  Folder policy & secrets
                </h2>
                <p className="mt-1 text-sm leading-6 text-slate-600 dark:text-zinc-400">
                  Secrets, triggers, checks, and path owners belong to folders in{" "}
                  <span className="font-mono">/{routeAccount}</span> rather than individual slices.
                  Open <span className="font-medium text-zinc-900 dark:text-zinc-100">Policy</span> on any
                  folder while browsing to inspect or add folder secrets, or manage all secrets at the account level.
                </p>
              </div>
              <Link
                className="shrink-0 rounded-md border border-slate-300 bg-white px-3 py-1.5 text-sm font-medium text-slate-700 transition hover:bg-slate-50 dark:border-zinc-700 dark:bg-zinc-900 dark:text-zinc-300 dark:hover:bg-zinc-950"
                params={{ account: routeAccount }}
                to="/accounts/$account/secrets"
              >
                Manage folder secrets
              </Link>
            </div>
          </SlicePanel>
        </div>
      ) : null}

      {canManageAccountSettings && settingsSlice ? (
        <div className="mt-8">
          <SliceGitMirrorPanel slice={settingsSlice} />
        </div>
      ) : null}
    </section>
  );
}
