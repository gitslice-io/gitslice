import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import { RpcError } from "../../api/errors";
import { SliceAccessNotice } from "./SliceAccessNotice";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children }: { children: ReactNode }) => <a href="#">{children}</a>
}));

describe("SliceAccessNotice", () => {
  afterEach(() => cleanup());

  it("asks a signed-out visitor to sign in instead of saying not found", () => {
    render(
      <SliceAccessNotice
        error={new RpcError(401, { code: "unauthenticated", message: "unauthenticated" })}
        isSignedIn={false}
        sliceKey="heibot/home"
      />
    );
    expect(screen.getByText("This slice is private")).toBeInTheDocument();
    expect(screen.getByText("Sign in")).toBeInTheDocument();
  });

  it("explains private slices and claims to a signed-in non-member", () => {
    render(
      <SliceAccessNotice
        error={new RpcError(404, { code: "not_found", message: "not found" })}
        isSignedIn
        sliceKey="heibot/home"
      />
    );
    expect(screen.getByText("No access to this slice")).toBeInTheDocument();
    expect(screen.getByText("claims page")).toBeInTheDocument();
  });

  it("shows other errors as errors", () => {
    render(
      <SliceAccessNotice error={new RpcError(503, { message: "unavailable" })} isSignedIn sliceKey="a/b" />
    );
    expect(screen.getByText("Could not load slice")).toBeInTheDocument();
    expect(screen.getByText("unavailable")).toBeInTheDocument();
  });
});
