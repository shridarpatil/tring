# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the Tring Go binaries. Build either binary with:
#
#   docker build --build-arg CMD=api          -t tring-api          .
#   docker build --build-arg CMD=orchestrator -t tring-orchestrator .
#
# Final images are distroless/static (~15 MB) running as non-root. CGO is
# off so the binary is statically linked — no glibc deps, no shell, no
# package manager in the runtime image.

ARG GO_VERSION=1.25

# ---- build stage ---------------------------------------------------------
# --platform=$BUILDPLATFORM pins the builder to the *runner's* native arch
# (always amd64 on a stock GitHub runner). Combined with GOARCH=$TARGETARCH
# below, this turns multi-arch builds into native Go cross-compilation
# instead of QEMU-emulated `go build`, which is ~10x faster for arm64.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS builder

ARG CMD=api
# TARGETOS and TARGETARCH are auto-populated by BuildKit to the target
# platform of the current build (e.g. linux/arm64). Go's cross-compiler
# reads these via the GOOS/GOARCH env vars below.
ARG TARGETOS
ARG TARGETARCH

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# -s -w strips DWARF + symbol tables; saves ~30% binary size.
# Trimpath removes absolute paths from binaries (reproducibility +
# slightly smaller binary).
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/tring ./cmd/${CMD}

# ---- runtime stage -------------------------------------------------------
FROM gcr.io/distroless/static-debian12:nonroot

# Distroless ships /etc/passwd with `nonroot` mapped to UID 65532.
USER nonroot:nonroot

COPY --from=builder /out/tring /tring

# Both binaries are configured entirely via env vars (TRING_DSN,
# TRING_ESL_ADDR, etc.). See .env.example for the full list.
ENTRYPOINT ["/tring"]
