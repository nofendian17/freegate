# Compile on the builder's native platform; only the runtime image is for
# TARGETPLATFORM. BuildKit supplies TARGETOS/TARGETARCH, so an arm64 image can
# be cross-compiled from an amd64 machine without QEMU.
#
# No `# syntax=` directive: BuildKit's bundled frontend supports the cache
# mounts below, and pinning a remote dockerfile frontend makes offline/air-
# gapped builds fail before they even reach these instructions.
FROM --platform=$BUILDPLATFORM golang:1.27 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /build

# Download modules in their own layer. The cache mount keeps downloads across
# rebuilds; locking avoids corruption when several builds share the daemon.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    go mod download

COPY . .
# The build cache survives source edits: only the changed packages recompile.
RUN --mount=type=cache,target=/go/pkg/mod,sharing=locked \
    --mount=type=cache,target=/root/.cache/go-build,sharing=locked \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /build/server ./cmd/server

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
COPY --from=builder /build/server /server
EXPOSE 1234
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget --no-verbose --tries=1 --spider http://localhost:1234/ready || exit 1
CMD ["/server"]
