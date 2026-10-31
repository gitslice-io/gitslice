import { accountHomeIsProfile } from "../lib/accounts";
import { useSelection } from "./selection";

// HomeTarget is where a Home link goes.
export interface HomeTarget {
  to: string;
  params?: { account: string };
}

// useHomeTarget is the active account's Home: your dashboard, or an
// organization's or agent's profile page. Linking there directly, rather than
// to "/" and its redirect, keeps a Home click on that page from flashing Home
// first. Without a selection (signed out, outside its provider), Home is "/".
export function useHomeTarget(): HomeTarget {
  let selection: ReturnType<typeof useSelection> | null = null;
  try {
    selection = useSelection();
  } catch {
    selection = null;
  }
  const account = selection?.activeAccount ?? "";
  if (account && accountHomeIsProfile(selection?.activeMembership?.kind ?? "personal")) {
    return { to: "/accounts/$account", params: { account } };
  }
  return { to: "/" };
}
