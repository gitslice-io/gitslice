# Multi-stage build for the gitslice core server (cmd/gitslice-server).
#
# The binary is a pure-Go, CGO-free static binary. The runtime image is
# distroless/static, which ships ca-certificates (needed for TLS to Neon,
# Cloudflare R2, and Clerk) and nothing else.
#
# On Cloud Run the server multiplexes gRPC + Connect + REST on a single port
# via h2c (see server.NewCombinedGRPCGatewayHandler). Bind it to 0.0.0.0 on the
# Cloud Run container port by setting GITSLICE_GRPC_ADDR=0.0.0.0:8080 and
# deploying with --use-http2 (see deploy/cloudrun.sh).

# ----- build stage -----
FROM golang:1.24 AS build
WORKDIR /src

# Cache module downloads first.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 for a fully static binary; trim symbols to shrink the image.
ENV CGO_ENABLED=0 GOOS=linux
RUN go build -trimpath -ldflags="-s -w" -o /out/gitslice-server ./cmd/gitslice-server

# ----- runtime stage -----
# The git smart-HTTP handler shells out to `git` and `git http-backend` (see
# internal/gitcompat), so the runtime image needs a real git install. Distroless
# ships no git, so use a minimal Debian base with git + ca-certificates (the
# latter is needed for TLS to Neon, R2, and Clerk).
FROM debian:bookworm-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends git ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/gitslice-server /usr/local/bin/gitslice-server

# Run unprivileged, mirroring the previous distroless :nonroot uid/gid (65532).
# HOME must be a writable dir so git has somewhere to resolve config; the git
# cache root also defaults under $TMPDIR (/tmp).
RUN groupadd --gid 65532 nonroot \
 && useradd --uid 65532 --gid 65532 --home-dir /home/nonroot --create-home --shell /usr/sbin/nologin nonroot
USER 65532:65532
ENV HOME=/home/nonroot

# Documented default; deploy/cloudrun.sh sets the real value. Cloud Run injects
# PORT=8080 by default and routes to the --port we configure (also 8080).
ENV GITSLICE_GRPC_ADDR=0.0.0.0:8080
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/gitslice-server"]
