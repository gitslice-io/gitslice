import { describe, expect, it } from "vitest";

import { getRouter } from "./router";

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
