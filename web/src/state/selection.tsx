import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { useAuth } from "@clerk/clerk-react";

import { useApi } from "../api/useApi";
import { hasMintedToken } from "../auth/token";

// The "account" is the signed-in user's own account, resolved from the session
// (GetAuthStatus) rather than typed in by the user. The first account is their
// personal account, whose home slice covers "/<account>".
interface SelectionState {
  account: string;
  accounts: string[];
  error: Error | null;
  isLoading: boolean;
  needsUsername: boolean;
  subjectId: string;
}

const SelectionContext = createContext<SelectionState | null>(null);

export function SelectionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const { isLoaded, isSignedIn } = useAuth();
  // A minted token is a complete session on its own and does not depend on
  // Clerk being loaded/signed in, so resolve the account from GetAuthStatus
  // whenever either auth path is ready.
  const mintedSession = hasMintedToken();
  const isAuthReady = mintedSession || (isLoaded && Boolean(isSignedIn));
  const { data, error, isError, isLoading } = useQuery({
    enabled: isAuthReady,
    queryKey: ["authStatus"],
    queryFn: () => api.getAuthStatus({})
  });

  const accounts = data?.accounts ?? [];
  const account = accounts[0] ?? "";
  const needsUsername = Boolean(data?.needsUsername);
  const subjectId = data?.subjectId ?? "";

  const value = useMemo<SelectionState>(
    () => ({
      account,
      accounts,
      error: isAuthReady && isError ? error : null,
      // For a minted-token session, loading tracks the GetAuthStatus query, not
      // Clerk's isLoaded (Clerk is irrelevant to that path).
      isLoading: mintedSession
        ? isAuthReady && isLoading
        : !isLoaded || (isAuthReady && isLoading),
      needsUsername: isAuthReady && needsUsername,
      subjectId
    }),
    [
      account,
      accounts.join(" "),
      error,
      isAuthReady,
      isLoaded,
      isError,
      isLoading,
      mintedSession,
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
