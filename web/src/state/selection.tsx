import { createContext, useContext, useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";

import { useApi } from "../api/useApi";

// The "account" is the signed-in user's own account, resolved from the session
// (GetAuthStatus) rather than typed in by the user. The first account is their
// personal account, whose home slice covers "/<account>".
interface SelectionState {
  account: string;
  accounts: string[];
  isLoading: boolean;
}

const SelectionContext = createContext<SelectionState | null>(null);

export function SelectionProvider({ children }: { children: ReactNode }) {
  const api = useApi();
  const { data, isLoading } = useQuery({
    queryKey: ["authStatus"],
    queryFn: () => api.getAuthStatus({})
  });

  const accounts = data?.accounts ?? [];
  const account = accounts[0] ?? "";

  const value = useMemo<SelectionState>(
    () => ({ account, accounts, isLoading }),
    [account, accounts.join(" "), isLoading]
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
