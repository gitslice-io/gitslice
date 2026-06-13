import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode
} from "react";

const ACCOUNT_KEY = "gitslice.web.account";

interface SelectionState {
  account: string;
  setAccount(account: string): void;
}

const SelectionContext = createContext<SelectionState | null>(null);

interface SelectionProviderProps {
  children: ReactNode;
}

export function SelectionProvider({ children }: SelectionProviderProps) {
  const initialAccount = useMemo(readInitialAccount, []);
  const [account, setAccountState] = useState(initialAccount);

  const setAccount = useCallback((nextAccount: string) => {
    setAccountState(nextAccount);
    localStorage.setItem(ACCOUNT_KEY, nextAccount);
  }, []);

  const value = useMemo(
    () => ({
      account,
      setAccount
    }),
    [account, setAccount]
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

function readInitialAccount() {
  const params = new URLSearchParams(window.location.search);
  const pathAccount = readSourceAccount(window.location.pathname);
  const storedAccount = localStorage.getItem(ACCOUNT_KEY) ?? "";

  return params.get("account") || pathAccount || storedAccount;
}

function readSourceAccount(pathname: string) {
  const match = pathname.match(/^\/source\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}
