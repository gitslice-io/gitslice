import type { ReactNode } from "react";

import type { ChangesetStack } from "../../api/types";
import { Link } from "@tanstack/react-router";
import { PageHeader } from "../../components/PageHeader";
import { primaryButtonClass, secondaryButtonClass } from "../stackPageUtils";
import { sliceRefLabel } from "../stackPageUtils";
import { stackDisplayName } from "../stackPageUtils";

export function StackHeader({
  breadcrumb,
  stack
}: {
  breadcrumb?: ReactNode;
  stack: ChangesetStack;
}) {
  const sliceLabel = sliceRefLabel(stack.authoringSlice) || "slice not returned";

  return (
    <>
      <PageHeader
        breadcrumb={breadcrumb}
        primaryAction={
          <div className="flex flex-wrap justify-start gap-2 lg:justify-end">
            <Link
              className={secondaryButtonClass}
              params={{ id: stack.id || "" }}
              to="/dependencies/$id/update"
            >
              Update dependents
            </Link>
            <Link
              className={primaryButtonClass}
              params={{ id: stack.id || "" }}
              to="/dependencies/$id/submit"
            >
              Submit dependencies
            </Link>
          </div>
        }
        title={
          <h1 className="truncate text-base font-semibold tracking-normal text-zinc-950 sm:text-lg">
            {stackDisplayName(stack)}
          </h1>
        }
      />
      <p className="mb-4 text-sm leading-6 text-slate-600">
        {`${sliceLabel} on ${stack.targetRef || "target ref not returned"}`}
      </p>
    </>
  );
}
