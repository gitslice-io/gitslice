import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode
} from "react";

const ACCOUNT_KEY = "gitslice.web.account";
const REF_KEY = "gitslice.web.ref";
const DEFAULT_REF = "main";

interface SelectionState {
  account: string;
  ref: string;
  setAccount(account: string): void;
  setRef(ref: string): void;
}

const SelectionContext = createContext<SelectionState | null>(null);

interface SelectionProviderProps {
  children: ReactNode;
}

export function SelectionProvider({ children }: SelectionProviderProps) {
  const initialSelection = useMemo(readInitialSelection, []);
  const [account, setAccountState] = useState(initialSelection.account);
  const [ref, setRefState] = useState(initialSelection.ref);

  const setAccount = useCallback((nextAccount: string) => {
    setAccountState(nextAccount);
    localStorage.setItem(ACCOUNT_KEY, nextAccount);
  }, []);

  const setRef = useCallback((nextRef: string) => {
    setRefState(nextRef);
    localStorage.setItem(REF_KEY, nextRef);
  }, []);

  const value = useMemo(
    () => ({
      account,
      ref,
      setAccount,
      setRef
    }),
    [account, ref, setAccount, setRef]
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

function readInitialSelection() {
  const params = new URLSearchParams(window.location.search);
  const pathAccount = readSourceAccount(window.location.pathname);
  const storedAccount = localStorage.getItem(ACCOUNT_KEY) ?? "";
  const storedRef = localStorage.getItem(REF_KEY) ?? DEFAULT_REF;

  return {
    account: params.get("account") || pathAccount || storedAccount,
    ref: params.get("ref") || storedRef || DEFAULT_REF
  };
}

function readSourceAccount(pathname: string) {
  const match = pathname.match(/^\/source\/([^/]+)/);
  return match ? decodeURIComponent(match[1]) : "";
}
