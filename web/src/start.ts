import { clerkMiddleware } from "@clerk/tanstack-react-start/server";
import { createStart } from "@tanstack/react-start";

const publishableKey = import.meta.env.VITE_CLERK_PUBLISHABLE_KEY;
const authorizedParties = (
  import.meta.env.VITE_CLERK_AUTHORIZED_PARTIES ?? ""
)
  .split(",")
  .map((origin) => origin.trim())
  .filter(Boolean);

export const startInstance = createStart(() => ({
  requestMiddleware: [clerkMiddleware({ authorizedParties, publishableKey })]
}));
