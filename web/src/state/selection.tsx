import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@clerk/clerk-react";

import { useApi } from "../api/useApi";
import { devAuthEnabled, getDevToken } from "../auth/devAuth";

// The "account" is the signed-in user's own account, resolved from the session
// (GetAuthStatus) rather than typed in by the user. The first account is their
// personal account, whose home slice covers "/<account>".
interface SelectionState {
  account: string;
  accounts: string[];
  error: Error | null;
  isLoading: boolean;
  subjectId: string;
}

const SelectionContext = createContext<SelectionState | null>(null);

export function SelectionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  const hasDevToken = devAuthEnabled && Boolean(getDevToken());
  const isAuthReady = hasDevToken || (isLoaded && Boolean(isSignedIn));
  const { data, error, isError, isLoading } = useQuery({
    enabled: isAuthReady,
    queryKey: ["authStatus"],
    queryFn: () => api.getAuthStatus({})
  });

  const accounts = data?.accounts ?? [];
  const account = accounts[0] ?? "";
  const subjectId = data?.subjectId ?? "";

  const value = useMemo<SelectionState>(
    () => ({
      account,
      accounts,
      error: isError ? error : null,
      isLoading: !isAuthReady || isLoading,
      subjectId
    }),
    [account, accounts.join(" "), error, isAuthReady, isError, isLoading, subjectId]
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
