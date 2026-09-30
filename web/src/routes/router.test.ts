import { describe, expect, it } from "vitest";

import {
  changesetTitle,
  docSectionTitle,
  getRouter,
  hasDehydratedAuthStatus,
  sliceTitle
} from "./router";

describe("login routing", () => {
  it.each([
    "/login",
    "/login/sso-callback",
    "/login/verify-email-address"
  ])(
    "mounts the Clerk sign-in flow at %s",
    (pathname) => {
      const matches = getRouter().matchRoutes(pathname);
      const leafMatch = matches[matches.length - 1];

      expect(leafMatch?.routeId).toBe("/login/$");
    }
  );
});

describe("route titles", () => {
  it("names doc sections and falls back for unknown ones", () => {
    expect(docSectionTitle("git-users")).toBe("For Git Users · Docs · Gitslice");
    expect(docSectionTitle("nope")).toBe("Docs · Gitslice");
    expect(docSectionTitle(undefined)).toBe("Docs · Gitslice");
  });

  it("leads slice titles with the selected file or folder", () => {
    expect(sliceTitle("nic", "home", undefined)).toBe("nic:home · Gitslice");
    expect(sliceTitle("nic", "home", "/nic/web/src/router.tsx")).toBe(
      "router.tsx · nic:home · Gitslice"
    );
  });

  it("prefers the changeset title when the server provided one", () => {
    expect(changesetTitle("a71c617ef9", "Fix the parser")).toBe(
      "Fix the parser · Changeset a71c617ef9 · Gitslice"
    );
    expect(changesetTitle("a71c617ef9", "")).toBe(
      "Changeset a71c617ef9 · Gitslice"
    );
  });
});

describe("hasDehydratedAuthStatus", () => {
  const query = (key: string, status: "success" | "error") =>
    ({ queryKey: [key], state: { status } }) as never;

  it("is true only for a successful authStatus query", () => {
    expect(
      hasDehydratedAuthStatus({
        mutations: [],
        queries: [query("globalRef", "success"), query("authStatus", "success")]
      })
    ).toBe(true);
    expect(
      hasDehydratedAuthStatus({
        mutations: [],
        queries: [query("authStatus", "error")]
      })
    ).toBe(false);
    expect(hasDehydratedAuthStatus({ mutations: [], queries: [] })).toBe(false);
    expect(hasDehydratedAuthStatus(undefined)).toBe(false);
  });
});

describe("code splitting", () => {
  it("loads every page component lazily", () => {
    const router = getRouter();
    const eager = Object.values(router.routesById)
      .filter((route) => route.id !== "__root__")
      .filter((route) => typeof route.options.component?.preload !== "function")
      .map((route) => route.id);

    expect(eager).toEqual([]);
  });
});
