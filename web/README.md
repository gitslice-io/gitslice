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
VITE_API_BASE_URL
```

The API client calls generated grpc-gateway unbound method paths such as
`POST /gitslice.core.v1.SliceService/ListSlices`.
