import { SlicesPage } from "./SlicesPage";

// Home is slice-first: after login, the user chooses the slice they want to
// inspect or author changesets against.
export function HomePage() {
  return <SlicesPage />;
}
