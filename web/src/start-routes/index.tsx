import { createFileRoute } from "@tanstack/react-router";

import { HomePage } from "../routes/HomePage";

export const Route = createFileRoute("/")({
  component: HomePage
});
