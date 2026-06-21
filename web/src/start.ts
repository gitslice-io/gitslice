import { clerkMiddleware } from "@clerk/tanstack-react-start/server";
import { createStart } from "@tanstack/react-start";

const publishableKey = import.meta.env.VITE_CLERK_PUBLISHABLE_KEY;

export const startInstance = createStart(() => ({
  requestMiddleware: [clerkMiddleware({ publishableKey })]
}));
