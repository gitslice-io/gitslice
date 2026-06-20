import { useAuth } from "@clerk/clerk-react";
import {
  useMutation,
  useQuery,
  useQueryClient
} from "@tanstack/react-query";
import { Link, useNavigate, useSearch } from "@tanstack/react-router";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";

import type { Changeset, SliceRef } from "../api/types";
import { type ApiClient, useApi } from "../api/useApi";
import { Breadcrumb, type Crumb } from "../components/Breadcrumb";
import { getErrorMessage } from "../components/slices/SlicePageParts";
import {
  Badge,
  Button,
  Card,
  Input,
  PageHeader,
  Surface
} from "../components/ui";
import { cn } from "../lib/cn";
import { shortChangesetId } from "../lib/objectId";
import { toSliceRouteParams } from "../lib/sliceRoutes";
import { displaySubmitBlockedReason } from "./stackPageUtils";

interface ChangesetsSearch {
  slice?: unknown;
}

type ChangesetsQueryKey = readonly ["changesets", string, string];

export function ChangesetsPage() {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const canManage = Boolean(isLoaded && isSignedIn);
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as ChangesetsSearch;
  const sliceRef = parseSliceSearch(search.slice);
  const account = sliceRef?.account ?? "";
  const slice = sliceRef?.slice ?? "";
  const queryKey = useMemo<ChangesetsQueryKey>(
    () => ["changesets", account, slice],
    [account, slice]
  );

  const changesetsQuery = useQuery({
    enabled: Boolean(account && slice),
    queryKey,
    queryFn: () =>
      api.listChangesets({
        authoringSlice: { account, slice }
      })
  });

  const changesets = useMemo(
    () => sortChangesets(changesetsQuery.data?.changesets ?? []),
    [changesetsQuery.data?.changesets]
  );

  const breadcrumbItems: Crumb[] = [{ label: "Slices", to: "/slices" }];
  if (sliceRef) {
    const routeParams = toSliceRouteParams(sliceRef);
    breadcrumbItems.push(
      routeParams
        ? {
            label: `${account}:${slice}`,
            to: "/slices/$account/$slice",
            params: routeParams
          }
        : { label: `${account}:${slice}` }
    );
  }
  breadcrumbItems.push({ label: "Changesets" });

  return (
    <section className="mx-auto w-full max-w-[100rem]">
      <div className="mb-4">
        <Breadcrumb items={breadcrumbItems} />
      </div>
      <PageHeader
        eyebrow="Changesets"
        title={sliceRef ? `${account}:${slice} · Changesets` : "Changesets"}
        description={
          sliceRef
            ? "Review and merge changesets authored against this slice."
            : "Open a slice and use its Changesets tab to see the slice-scoped review queue."
        }
      />
      <div className="mt-6">
        {!sliceRef ? (
          <MissingSliceState navigateToChangeset={navigateToChangeset(navigate)} />
        ) : changesetsQuery.isLoading ? (
          <ChangesetsLoadingBlock />
        ) : changesetsQuery.isError ? (
          <NoticeCard title="Could not load changesets" tone="error">
            {getErrorMessage(changesetsQuery.error)}
          </NoticeCard>
        ) : changesets.length === 0 ? (
          <NoticeCard title="No changesets for this slice yet.">
            New changesets created from the slice workspace will appear here.
          </NoticeCard>
        ) : (
          <ChangesetsTable
            api={api}
            canManage={canManage}
            changesets={changesets}
            queryKey={queryKey}
          />
        )}
      </div>
    </section>
  );
}

function ChangesetsTable({
  api,
  canManage,
  changesets,
  queryKey
}: {
  api: ApiClient;
  canManage: boolean;
  changesets: Changeset[];
  queryKey: ChangesetsQueryKey;
}) {
  return (
    <Surface className="overflow-hidden p-2" level="lowest">
      <div className="overflow-x-auto">
        <table className="min-w-full border-separate border-spacing-y-2 text-left text-sm">
          <thead className="font-label text-xs font-semibold uppercase text-on-surface-variant">
            <tr>
              <th className="px-3 py-2 sm:px-4">Changeset</th>
              <th className="hidden px-3 py-2 sm:table-cell sm:px-4">Status</th>
              <th className="hidden px-4 py-2 md:table-cell">Author</th>
              <th className="hidden px-4 py-2 md:table-cell">Approvals</th>
              <th className="hidden px-4 py-2 md:table-cell">
                Blocked reason
              </th>
              {canManage ? (
                <th className="px-3 py-2 text-right sm:px-4">Actions</th>
              ) : null}
            </tr>
          </thead>
          <tbody>
            {changesets.map((changeset) => (
              <ChangesetRow
                api={api}
                canManage={canManage}
                changeset={changeset}
                key={changeset.id || changeset.number}
                queryKey={queryKey}
              />
            ))}
          </tbody>
        </table>
      </div>
    </Surface>
  );
}

function ChangesetRow({
  api,
  canManage,
  changeset,
  queryKey
}: {
  api: ApiClient;
  canManage: boolean;
  changeset: Changeset;
  queryKey: ChangesetsQueryKey;
}) {
  const queryClient = useQueryClient();
  const changesetId = changeset.id ?? "";
  const detailId = shortChangesetId(changesetId) || changesetId;
  const label = changesetLabel(changeset);
  const mergeable = isMergeableStatus(changeset.status);
  const [rowError, setRowError] = useState("");

  const invalidateList = async () => {
    await queryClient.invalidateQueries({ queryKey });
  };

  const mergeMutation = useMutation({
    mutationFn: async () => {
      if (!changesetId) {
        throw new Error("This changeset did not return an id.");
      }

      return api.submitChangeset({
        changesetId,
        expectedCurrentPatchsetId: changeset.currentPatchsetId
      });
    },
    onError: (error) => setRowError(getErrorMessage(error)),
    onMutate: () => setRowError(""),
    onSuccess: async () => {
      setRowError("");
      await invalidateList();
    }
  });

  const busy = mergeMutation.isPending;

  return (
    <tr className="group align-top">
      <td
        className={cn(
          changesetRowCellClass,
          "max-w-[14rem] px-3 py-4 sm:min-w-72 sm:max-w-none sm:px-4"
        )}
      >
        <div className="flex flex-col gap-1">
          <div className="sm:hidden">
            <StatusBadge status={changeset.status} />
          </div>
          {detailId ? (
            <Link
              className="group min-w-0"
              params={{ id: detailId }}
              to="/cs/$id"
            >
              <span className="block break-words font-semibold text-on-surface underline decoration-primary/30 underline-offset-4 group-hover:decoration-primary">
                {label}
              </span>
              <span className="mt-1 block max-w-xl break-words text-sm text-on-surface-variant group-hover:text-on-surface">
                {changeset.title || "Untitled changeset"}
              </span>
            </Link>
          ) : (
            <>
              <span className="font-semibold text-on-surface">{label}</span>
              <span className="max-w-xl break-words text-sm text-on-surface-variant">
                {changeset.title || "Untitled changeset"}
              </span>
            </>
          )}
          {changeset.affectedPaths?.length ? (
            <span className="max-w-xl break-all font-mono text-xs text-on-surface-muted sm:truncate">
              {changeset.affectedPaths.join(", ")}
            </span>
          ) : null}
          {changeset.submitBlockedReason ? (
            <p className="mt-2 rounded-sm bg-tertiary-container px-2 py-1 text-xs leading-5 text-tertiary md:hidden">
              {displaySubmitBlockedReason(changeset.submitBlockedReason)}
            </p>
          ) : null}
          {rowError ? (
            <p className="mt-2 rounded-sm bg-rose-50 px-2 py-1 text-xs leading-5 text-rose-800 md:hidden">
              {rowError}
            </p>
          ) : null}
        </div>
      </td>
      <td
        className={cn(
          changesetRowCellClass,
          "hidden px-3 py-4 sm:table-cell sm:px-4"
        )}
      >
        <StatusBadge status={changeset.status} />
      </td>
      <td
        className={cn(
          changesetRowCellClass,
          "hidden px-4 py-4 text-on-surface-variant md:table-cell"
        )}
      >
        {changeset.author || "not returned"}
      </td>
      <td
        className={cn(
          changesetRowCellClass,
          "hidden px-4 py-4 text-on-surface-variant md:table-cell"
        )}
      >
        {changeset.submitRequirements?.requiredApprovals ?? "not returned"}
      </td>
      <td
        className={cn(
          changesetRowCellClass,
          "hidden max-w-sm px-4 py-4 text-on-surface-variant md:table-cell"
        )}
      >
        {changeset.submitBlockedReason ? (
          <span>{displaySubmitBlockedReason(changeset.submitBlockedReason)}</span>
        ) : (
          <span className="text-on-surface-muted">None</span>
        )}
        {rowError ? (
          <p className="mt-2 rounded-sm bg-rose-50 px-3 py-2 text-sm text-rose-800">
            {rowError}
          </p>
        ) : null}
      </td>
      {canManage ? (
        <td className={cn(changesetRowCellClass, "px-3 py-4 sm:px-4")}>
          <div className="flex flex-wrap justify-end gap-2">
            <Button
              disabled={busy || !changesetId || !mergeable}
              onClick={() => mergeMutation.mutate()}
              size="sm"
              type="button"
            >
              {mergeMutation.isPending ? "Merging..." : "Merge"}
            </Button>
          </div>
        </td>
      ) : null}
    </tr>
  );
}

function MissingSliceState({
  navigateToChangeset
}: {
  navigateToChangeset(id: string): void;
}) {
  return (
    <div className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_22rem]">
      <NoticeCard title="Open a slice first">
        Use a slice page&apos;s Changesets tab to open the slice-scoped list.
      </NoticeCard>
      <OpenChangesetForm onOpen={navigateToChangeset} />
    </div>
  );
}

function OpenChangesetForm({ onOpen }: { onOpen(id: string): void }) {
  const [changeset, setChangeset] = useState("");
  const [error, setError] = useState("");

  const submit = (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault();
    const id = changeset.trim();

    if (!id) {
      setError("Enter a changeset id.");
      return;
    }

    setError("");
    onOpen(id);
  };

  return (
    <Card level="low" padding="md">
      <form onSubmit={submit}>
        <label className="grid gap-2 font-label text-sm font-semibold text-on-surface">
          Open changeset
          <Input
            error={Boolean(error)}
            onChange={(event) => setChangeset(event.target.value)}
            placeholder="3f9a2b1c4d"
            value={changeset}
          />
        </label>

        {error ? <p className="mt-2 text-sm text-rose-700">{error}</p> : null}

        <Button className="mt-5" type="submit">
          Open
        </Button>
      </form>
    </Card>
  );
}

function NoticeCard({
  children,
  title,
  tone = "neutral"
}: {
  children?: ReactNode;
  title: string;
  tone?: "neutral" | "error";
}) {
  return (
    <Card
      className={cn(
        "text-sm",
        tone === "error"
          ? "bg-rose-50 text-rose-800"
          : "text-on-surface-variant"
      )}
      level={tone === "error" ? "high" : "low"}
      padding="md"
    >
      <p className="font-label font-semibold text-on-surface">{title}</p>
      {children ? <div className="mt-1 leading-6">{children}</div> : null}
    </Card>
  );
}

function ChangesetsLoadingBlock() {
  return (
    <div className="space-y-4">
      <div className="h-8 w-56 animate-pulse rounded-sm bg-surface-container-high" />
      <Card level="low" padding="md">
        <div className="h-4 w-3/4 animate-pulse rounded-sm bg-surface-container-high" />
        <div className="mt-4 h-4 w-1/2 animate-pulse rounded-sm bg-surface-container-high" />
        <div className="mt-4 h-4 w-2/3 animate-pulse rounded-sm bg-surface-container-high" />
      </Card>
    </div>
  );
}

function navigateToChangeset(navigate: ReturnType<typeof useNavigate>) {
  return (id: string) => {
    void navigate({
      params: { id },
      to: "/cs/$id"
    });
  };
}

function parseSliceSearch(value: unknown): Required<SliceRef> | null {
  if (typeof value !== "string") {
    return null;
  }

  const trimmed = value.trim();
  // Canonical handle is account:slice; also accept the legacy account/slice.
  const sep = trimmed.includes(":") ? ":" : "/";
  const index = trimmed.indexOf(sep);

  if (index <= 0) {
    return null;
  }

  const account = trimmed.slice(0, index).trim();
  const slice = trimmed.slice(index + 1).trim();

  if (!account || !slice) {
    return null;
  }

  return { account, slice };
}

function sortChangesets(changesets: Changeset[]) {
  return [...changesets].sort((left, right) => {
    const rightNumber = changesetNumberValue(right);
    const leftNumber = changesetNumberValue(left);

    if (rightNumber !== leftNumber) {
      return rightNumber - leftNumber;
    }

    return (right.id || "").localeCompare(left.id || "");
  });
}

function changesetNumberValue(changeset: Changeset) {
  const value = Number(changeset.number);
  return Number.isFinite(value) ? value : 0;
}

function changesetLabel(changeset: Changeset) {
  const shortId = shortChangesetId(changeset.id || "");
  if (shortId) {
    return shortId;
  }
  if (changeset.number !== undefined && changeset.number !== "") {
    return `#${changeset.number}`;
  }
  return changeset.id || "Changeset";
}

function StatusBadge({ status }: { status?: string }) {
  return (
    <Badge size="md" variant={statusVariant(status)}>
      {status || "unknown"}
    </Badge>
  );
}

function statusVariant(status?: string) {
  switch ((status || "").toLowerCase()) {
    case "published":
    case "merged":
    case "submitted":
      return "primary";
    case "pending_publish":
    case "draft":
    case "open":
      return "tertiary";
    case "abandoned":
    default:
      return "neutral";
  }
}

const changesetRowCellClass =
  "bg-surface-container-low transition-colors first:rounded-l-sm last:rounded-r-sm group-hover:bg-surface-container";

// Submit (merge) is only valid while the changeset is still open. Once it has
// been submitted/published/abandoned it can't be merged again.
function isMergeableStatus(status?: string) {
  const normalized = (status || "").toLowerCase();
  return normalized === "" || normalized === "draft" || normalized === "open";
}
