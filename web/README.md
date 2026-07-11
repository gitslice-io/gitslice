# Gitslice Web

This directory contains the React/Vite browser application for the Gitslice web
surface. It is currently a foundation scaffold: shared auth, routing, API
transport, app shell, and stub pages are present so page bodies can be filled in
incrementally.

Run it locally with:

```bash
npm install
npm run dev
```

Required environment:

```text
VITE_CLERK_PUBLISHABLE_KEY
VITE_CLERK_AUTHORIZED_PARTIES
VITE_API_BASE_URL
```

The API client uses ConnectRPC over HTTP with TypeScript descriptors generated
from `proto/core/v1/*.proto` into `src/gen/`. The browser does not use the
grpc-gateway JSON routes. Regenerate the generated files from the repository
root with:

```bash
make proto
```
