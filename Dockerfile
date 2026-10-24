# syntax=docker/dockerfile:1.7
#
# Multi-stage build for the Tring Go binaries. Build any binary with:
#
#   docker build --build-arg CMD=api      -t tring-api     .
#   docker build --build-arg CMD=gateway  -t tring-gateway .
#
# Final images are distroless/cc running as non-root. CGO is off for the api, so
# it is statically linked; the gateway links libopus (cgo) to mix a supervisor's
# barge into phone calls. The runtime carries glibc for the gateway and for the
# bundled Piper TTS (+ its onnxruntime). No shell, no package manager in the image.

ARG GO_VERSION=1.26
ARG NODE_VERSION=22

# ---- web UI build --------------------------------------------------------
# Builds the Vue admin console; its dist/ is consumed by the Go embed step
# below. Cheap to always run — the gateway binary doesn't import the
# webui package so the dist bytes don't end up in its image either way.
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS web

WORKDIR /web

# Lock-file first for cacheable npm install.
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

COPY web/ ./
# Vite's outDir (in vite.config.ts) points at ../internal/webui/dist —
# i.e. /internal/webui/dist inside this container. After build runs, that
# path holds index.html + the hashed assets/ tree.
RUN npm run build

# ---- browser widget build ------------------------------------------------
# Builds the drop-in JS widgets (voice.js / agent.js / tring.js). Their
# bundles are embedded by the Go build below and served at /sdk/* on the
# same origin as the API, so self-hosters need no CDN. The gateway
# binary doesn't import internal/widget, so the bytes don't reach its image.
FROM --platform=$BUILDPLATFORM node:${NODE_VERSION}-alpine AS sdk

WORKDIR /sdk
COPY sdk/js/package.json sdk/js/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm \
    npm ci

COPY sdk/js/ ./
# Build, then copy each drop-in bundle to its served name under /widget-dist.
RUN npm run build && \
    mkdir -p /widget-dist && \
    cp dist/voice.iife.js /widget-dist/voice.js && \
    cp dist/agent.iife.js /widget-dist/agent.js && \
    cp dist/tring.iife.js /widget-dist/tring.js

# ---- Go build stage ------------------------------------------------------
# --platform=$BUILDPLATFORM pins the builder to the *runner's* native arch
# (always amd64 on a stock GitHub runner). Combined with GOARCH=$TARGETARCH
# below, this turns multi-arch builds into native Go cross-compilation
# instead of QEMU-emulated `go build`, which is ~10x faster for arm64.
# Debian bookworm, not alpine: the gateway's libopus must match the glibc of
# the distroless/cc-debian12 runtime.
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-bookworm AS builder

ARG CMD=api
# VERSION is stamped into the api binary (main.version) and shown in the
# console footer. CI passes the tag, or develop-<short sha> for :develop.
ARG VERSION=dev
# TARGETOS and TARGETARCH are auto-populated by BuildKit to the target
# platform of the current build (e.g. linux/arm64). Go's cross-compiler
# reads these via the GOOS/GOARCH env vars below.
ARG TARGETOS
ARG TARGETARCH
ARG BUILDARCH

# The gateway only: a C compiler for the target arch and the target's libopus
# (Debian multiarch), so arm64 stays a native cross-build with cgo on.
RUN set -eux; \
    if [ "$CMD" = gateway ]; then \
      case "$TARGETARCH" in \
        amd64) cross=gcc-x86-64-linux-gnu ;; \
        arm64) cross=gcc-aarch64-linux-gnu ;; \
        *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
      esac; \
      if [ "$TARGETARCH" = "$BUILDARCH" ]; then cross=gcc; fi; \
      dpkg --add-architecture "$TARGETARCH"; \
      apt-get update; \
      apt-get install -y --no-install-recommends "$cross" pkg-config "libc6-dev:$TARGETARCH" "libopus-dev:$TARGETARCH"; \
      rm -rf /var/lib/apt/lists/*; \
    fi

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

# Drop the compiled UI dist into the embed source dir so //go:embed picks
# it up. Overwrites the .gitkeep placeholder. The api binary's bundle
# will include index.html + assets.
COPY --from=web /internal/webui/dist /src/internal/webui/dist

# Drop the compiled widget bundles into the embed source dir so the api
# binary serves them at /sdk/*. Overwrites the .gitkeep placeholder.
COPY --from=sdk /widget-dist /src/internal/widget/dist

# -s -w strips DWARF + symbol tables; saves ~30% binary size.
# Trimpath removes absolute paths from binaries (reproducibility +
# slightly smaller binary). The gateway is built with cgo and `-tags opus` so
# ports can mix (internal/media/sfu/mix.go; nolibopusfile: only libopus is
# needed), and its libopus.so.0 is staged in /out/lib for the runtime.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eux; \
    mkdir -p /out/lib; \
    if [ "$CMD" = gateway ]; then \
      case "$TARGETARCH" in \
        amd64) triple=x86_64-linux-gnu ;; \
        arm64) triple=aarch64-linux-gnu ;; \
      esac; \
      CGO_ENABLED=1 CC="$triple-gcc" PKG_CONFIG_PATH="/usr/lib/$triple/pkgconfig" \
      GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath -tags "opus nolibopusfile" -ldflags="-s -w" -o /out/tring ./cmd/gateway; \
      cp -L "/usr/lib/$triple/libopus.so.0" /out/lib/; \
    else \
      CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
      go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/tring ./cmd/${CMD}; \
    fi

# ---- piper TTS stage -----------------------------------------------------
# Fetches the Piper release (self-contained: binary + libonnxruntime +
# libpiper_phonemize + espeak-ng-data) and one default voice model. Embedded in
# the runtime so the API can synthesize IVR prompt text to audio on flow save.
# Piper is glibc-built, hence the distroless/cc runtime below. Layers are
# content-addressed, so the registry stores piper once even though all three
# images share this runtime. (Only the API uses it; the others just carry it.)
FROM --platform=$BUILDPLATFORM alpine:3.20 AS piper
ARG TARGETARCH
ARG PIPER_VERSION=2023.11.14-2
# Voice: rhasspy/piper-voices. Stored as voice.onnx(+.json) to match the config
# default tts_piper_model=/usr/local/share/piper/voice.onnx.
ARG PIPER_VOICE_URL=https://huggingface.co/rhasspy/piper-voices/resolve/main/en/en_US/lessac/medium/en_US-lessac-medium
RUN apk add --no-cache curl tar
RUN set -eux; \
    case "$TARGETARCH" in \
      amd64) parch=x86_64 ;; \
      arm64) parch=aarch64 ;; \
      *) echo "unsupported TARGETARCH: $TARGETARCH" >&2; exit 1 ;; \
    esac; \
    curl -fsSL "https://github.com/rhasspy/piper/releases/download/${PIPER_VERSION}/piper_linux_${parch}.tar.gz" \
      | tar -xz -C /opt; \
    mkdir -p /usr/local/share/piper; \
    curl -fsSL "${PIPER_VOICE_URL}.onnx"      -o /usr/local/share/piper/voice.onnx; \
    curl -fsSL "${PIPER_VOICE_URL}.onnx.json" -o /usr/local/share/piper/voice.onnx.json; \
    /opt/piper/piper --help >/dev/null 2>&1 || true   # smoke (may fail on musl; real check is in cc runtime)

# ---- runtime stage -------------------------------------------------------
# distroless/cc (glibc + libstdc++) rather than /static, because Piper and its
# onnxruntime are dynamically linked. The static ffmpeg still runs fine here.
FROM gcr.io/distroless/cc-debian12:nonroot

# A statically-linked ffmpeg (runs in distroless — no shell/libc needed). Used to
# transcode uploaded prompts + synthesized TTS to Opus-Ogg (api) and merge stereo
# recordings (gateway). /usr/local/bin is on the distroless PATH.
COPY --from=mwader/static-ffmpeg:7.1 /ffmpeg /usr/local/bin/ffmpeg

# Piper binary (+ its bundled .so's and espeak-ng-data) and the default voice.
# LD_LIBRARY_PATH lets the binary resolve its sibling shared libs; espeak-ng-data
# is found relative to the binary.
COPY --from=piper /opt/piper /opt/piper
COPY --from=piper /usr/local/share/piper /usr/local/share/piper
# libopus for the gateway (empty for the api).
COPY --from=builder /out/lib/ /opt/tring/lib/
ENV LD_LIBRARY_PATH=/opt/piper:/opt/tring/lib

# Distroless ships /etc/passwd with `nonroot` mapped to UID 65532.
USER nonroot:nonroot

COPY --from=builder /out/tring /tring

# Both binaries are configured entirely via env vars (TRING_DSN,
# TRING_ESL_ADDR, etc.). See .env.example for the full list.
ENTRYPOINT ["/tring"]
