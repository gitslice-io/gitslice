import type { ChangesetStack } from "../../api/types";
import { Link } from "@tanstack/react-router";
import { primaryButtonClass, secondaryButtonClass } from "../stackPageUtils";
import { sliceRefLabel } from "../stackPageUtils";
import { SlicePageHeader } from "../../components/slices/SlicePageParts";
import { shortStackId, stackDisplayName } from "../stackPageUtils";

export function StackHeader({ stack }: { stack: ChangesetStack }) {
  const sliceLabel = sliceRefLabel(stack.authoringSlice) || "slice not returned";

  return (
    <SlicePageHeader
      actions={
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
      description={`${sliceLabel} on ${stack.targetRef || "target ref not returned"}`}
      eyebrow="Dependencies"
      title={stackDisplayName(stack)}
    />
  );
}