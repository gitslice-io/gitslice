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
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gitslice-server /usr/local/bin/gitslice-server

# Documented default; deploy/cloudrun.sh sets the real value. Cloud Run injects
# PORT=8080 by default and routes to the --port we configure (also 8080).
ENV GITSLICE_GRPC_ADDR=0.0.0.0:8080
EXPOSE 8080

ENTRYPOINT ["/usr/local/bin/gitslice-server"]
