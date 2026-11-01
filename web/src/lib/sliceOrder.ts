import type { Slice } from "../api/types";

// How an account page lists its slices. The home slice always comes first.
export type SliceSort = "name" | "active" | "newest";

export const sliceSorts: { value: SliceSort; label: string }[] = [
  { value: "name", label: "Name" },
  { value: "active", label: "Recently active" },
  { value: "newest", label: "Newest" }
];

const STORAGE_KEY = "gitslice.sliceSort";

export function isSliceSort(value: unknown): value is SliceSort {
  return sliceSorts.some((sort) => sort.value === value);
}

// The viewer's last chosen order, remembered in this browser only.
export function loadSliceSort(): SliceSort {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    return isSliceSort(stored) ? stored : "name";
  } catch {
    return "name";
  }
}

export function saveSliceSort(sort: SliceSort) {
  try {
    window.localStorage.setItem(STORAGE_KEY, sort);
  } catch {
    // Not remembered: private windows and blocked storage.
  }
}

// isHomeSlice: every personal account's own slice over its whole root
// (design/12_account_auth.md section 5.1).
export function isHomeSlice(slice: Slice) {
  return slice.ref?.slice === "home";
}

function sliceName(slice: Slice) {
  return slice.ref?.slice ?? slice.id ?? "";
}

function time(value?: string) {
  const at = value ? Date.parse(value) : NaN;
  return Number.isNaN(at) ? 0 : at;
}

// sortSlices orders slices by sort, the home slice first, ties by name.
export function sortSlices(slices: Slice[], sort: SliceSort): Slice[] {
  const key = (slice: Slice) =>
    sort === "active" ? time(slice.lastActivityAt || slice.createdAt) : sort === "newest" ? time(slice.createdAt) : 0;
  return [...slices].sort(
    (a, b) =>
      Number(isHomeSlice(b)) - Number(isHomeSlice(a)) ||
      key(b) - key(a) ||
      sliceName(a).localeCompare(sliceName(b))
  );
}
