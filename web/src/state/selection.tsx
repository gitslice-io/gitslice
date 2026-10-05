import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@clerk/tanstack-react-start";

import { useApi } from "../api/useApi";
import { ACTIVE_ACCOUNT_KEY, membershipFor, normalizeMemberships, type Membership } from "../lib/accounts";

// The "account" is the signed-in user's own account, resolved from the session
// (GetAuthStatus) rather than typed in by the user. The first account is their
// personal account, whose home slice covers "/<account>".
//
// The "active account" is the context the viewer works in, as on GitHub: their
// personal account or an organization (or agent) they belong to. Home lists
// its slices and new slices go there. It is remembered in this browser.
interface SelectionState {
  account: string;
  accounts: string[];
  memberships: Membership[];
  activeAccount: string;
  activeMembership: Membership | undefined;
  setActiveAccount(account: string): void;
  error: Error | null;
  isLoading: boolean;
  needsUsername: boolean;
  subjectId: string;
}

const SelectionContext = createContext<SelectionState | null>(null);

export function SelectionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const isAuthReady = isLoaded && Boolean(isSignedIn);
  const shouldLoadAuthStatus = isAuthReady;
  const { data, error, isError, isLoading } = useQuery({
    enabled: shouldLoadAuthStatus,
    queryKey: ["authStatus"],
    queryFn: () => api.getAuthStatus({})
  });

  const accounts = data?.accounts ?? [];
  const account = accounts[0] ?? "";
  const memberships = useMemo(
    () => normalizeMemberships(data?.accounts ?? [], data?.memberships),
    [data]
  );
  const [chosen, setChosen] = useState("");

  // Read after hydration: the server render has no browser storage.
  useEffect(() => {
    try {
      setChosen(window.localStorage.getItem(ACTIVE_ACCOUNT_KEY) ?? "");
    } catch {
      // Storage can be unavailable (private windows, blocked site data).
    }
  }, []);

  const setActiveAccount = useCallback((next: string) => {
    setChosen(next);
    try {
      window.localStorage.setItem(ACTIVE_ACCOUNT_KEY, next);
    } catch {
      // Only this tab remembers it then.
    }
  }, []);

  // A remembered account the viewer has since left falls back to their own.
  const activeMembership = membershipFor(memberships, chosen) ?? memberships[0];
  const activeAccount = activeMembership?.account ?? account;
  const needsUsername = Boolean(data?.needsUsername);
  const subjectId = data?.subjectId ?? "";

  const value = useMemo<SelectionState>(
    () => ({
      account,
      accounts,
      memberships,
      activeAccount,
      activeMembership,
      setActiveAccount,
      error: isAuthReady && isError ? error : null,
      isLoading: !isLoaded || (isAuthReady && isLoading),
      needsUsername: isAuthReady && needsUsername,
      subjectId
    }),
    [
      account,
      accounts.join(" "),
      memberships,
      activeAccount,
      activeMembership,
      setActiveAccount,
      error,
      isAuthReady,
      isLoaded,
      isError,
      isLoading,
      needsUsername,
      subjectId
    ]
  );

  return (
    <SelectionContext.Provider value={value}>
      {children}
    </SelectionContext.Provider>
  );
}

export function useSelection() {
  const value = useContext(SelectionContext);

  if (!value) {
    throw new Error("useSelection must be used inside SelectionProvider");
  }

  return value;
}
