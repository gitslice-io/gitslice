import { renderHook } from "@testing-library/react";
import type { ReactNode } from "react";
import { describe, expect, it } from "vitest";

import { useHomeTarget } from "./homeTarget";
import { SelectionContext } from "./selection";

function wrapperFor(value: unknown) {
  return ({ children }: { children: ReactNode }) => (
    <SelectionContext.Provider value={value as never}>{children}</SelectionContext.Provider>
  );
}

describe("useHomeTarget", () => {
  it("is an organization's profile while it is the active account", () => {
    const { result } = renderHook(() => useHomeTarget(), {
      wrapper: wrapperFor({ activeAccount: "gitslice", activeMembership: { account: "gitslice", kind: "organization", role: "owner" } })
    });
    expect(result.current).toEqual({ to: "/accounts/$account", params: { account: "gitslice" } });
  });

  it("is the dashboard for your personal account, and when signed out", () => {
    const personal = renderHook(() => useHomeTarget(), {
      wrapper: wrapperFor({ activeAccount: "nic", activeMembership: { account: "nic", kind: "personal", role: "owner" } })
    });
    expect(personal.result.current).toEqual({ to: "/" });
    expect(renderHook(() => useHomeTarget()).result.current).toEqual({ to: "/" });
  });
});
