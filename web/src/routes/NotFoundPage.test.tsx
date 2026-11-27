import "@testing-library/jest-dom/vitest";

import { cleanup, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, describe, expect, it, vi } from "vitest";

import {
  RpcError,
  isDefiniteClientError,
  isNotFoundError,
  isPermissionDeniedError,
  isUnauthenticatedError
} from "../api/rpcError";

vi.mock("@tanstack/react-router", () => ({
  Link: ({ children, to, ...rest }: { children: ReactNode; to: string }) => (
    <a {...(rest as Record<string, unknown>)} href={to}>
      {children}
    </a>
  )
}));

import { AccountNotFound, ChangesetNotFound, PageNotFound, SliceNotFound } from "./NotFoundPage";

afterEach(cleanup);

describe("not found pages", () => {
  it("says what is missing, with a way back and a way to sign in", () => {
    for (const [Page, title] of [
      [PageNotFound, "Page not found"],
      [SliceNotFound, "No such slice"],
      [ChangesetNotFound, "No such changeset"],
      [AccountNotFound, "No such account"]
    ] as const) {
      render(<Page />);
      expect(screen.getByRole("heading", { level: 1, name: title })).toBeInTheDocument();
      expect(screen.getByRole("link", { name: "Go to Gitslice" })).toHaveAttribute("href", "/");
      expect(screen.getByRole("link", { name: "Sign in" })).toHaveAttribute("href", "/login");
      cleanup();
    }
  });

  it("hints that a slice or changeset may be private, since the server does not say", () => {
    render(<SliceNotFound />);
    expect(screen.getByText(/may be private/)).toBeInTheDocument();
  });
});

describe("isNotFoundError", () => {
  it("is true for the server's not-found answers and nothing else", () => {
    expect(isNotFoundError(new RpcError(404, { code: "not_found", message: "not found" }))).toBe(true);
    expect(isNotFoundError(new RpcError(404))).toBe(true);
    expect(isNotFoundError(new RpcError(500, { code: 5 }))).toBe(true);
    expect(isNotFoundError(new RpcError(401, { code: "unauthenticated" }))).toBe(false);
    expect(isNotFoundError(new RpcError(503, { code: "unavailable" }))).toBe(false);
    expect(isNotFoundError(new Error("not found"))).toBe(false);
    expect(isNotFoundError(undefined)).toBe(false);
  });
});

describe("rpcError classifiers", () => {
  it("classifies unauthenticated, permission-denied, and definite client errors", () => {
    const unauth = new RpcError(401, { code: 16, message: "missing subject" });
    const unauthByCode = new RpcError(500, { code: "unauthenticated" });
    const denied = new RpcError(403, { code: 7, message: "forbidden" });
    const deniedByCode = new RpcError(500, { code: "PermissionDenied" });
    const badReq = new RpcError(400, { code: 3, message: "invalid" });
    const unavailable = new RpcError(503, { code: "unavailable" });

    expect(isUnauthenticatedError(unauth)).toBe(true);
    expect(isUnauthenticatedError(unauthByCode)).toBe(true);
    expect(isUnauthenticatedError(denied)).toBe(false);

    expect(isPermissionDeniedError(denied)).toBe(true);
    expect(isPermissionDeniedError(deniedByCode)).toBe(true);
    expect(isPermissionDeniedError(unauth)).toBe(false);

    expect(isDefiniteClientError(badReq)).toBe(true);
    expect(isDefiniteClientError(unauth)).toBe(true);
    expect(isDefiniteClientError(denied)).toBe(true);
    expect(isDefiniteClientError(new RpcError(404))).toBe(true);
    expect(isDefiniteClientError(unavailable)).toBe(false);
    expect(isDefiniteClientError(new Error("boom"))).toBe(false);
  });
});
