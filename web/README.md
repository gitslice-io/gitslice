# Gitslice Web

This directory contains the browser application for the prototype web surface.
The current implementation is intentionally limited to the CLI signup approval
page.

Run it locally with:

```bash
make run-web
```

The signup page calls the generated grpc-gateway endpoint:

```text
POST /gitslice.core.v1.FakeAccountService/ApproveSignup
```

The Go server does not mount custom web handlers for this page. It only exposes
the HTTP JSON gateway when `GITSLICE_HTTP_ADDR` or `--http-addr` is configured.
