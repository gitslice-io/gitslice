import type { Slice } from "../../api/types";
import { coveringSlices, sliceLabel } from "./sourceUtils";

interface SourceCoverageProps {
  error: Error | null;
  isLoading: boolean;
  repositoryPath: string;
  slices: Slice[];
}

export function SourceCoverage({
  error,
  isLoading,
  repositoryPath,
  slices
}: SourceCoverageProps) {
  const covering = coveringSlices(slices, repositoryPath);

  return (
    <div className="rounded-lg border border-slate-200 bg-white px-4 py-3 text-sm">
      <span className="font-semibold text-slate-700">Covering slices: </span>
      {isLoading ? (
        <span className="text-slate-500">loading</span>
      ) : error ? (
        <span className="text-amber-700">{error.message}</span>
      ) : covering.length > 0 ? (
        <span className="text-slate-600">
          {covering.map((slice) => sliceLabel(slice)).join(", ")}
        </span>
      ) : (
        <span className="text-slate-500">none</span>
      )}
    </div>
  );
}

