# syntax=docker/dockerfile:1

# --- frontend ---
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /w
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# --- binary ---
# The toolchain is pinned to match go.mod. tailscale.com pulls in gVisor
# netstack, so expect a slow cold build and a ~30 MB binary; that is the cost of
# joining the tailnet in-process instead of running tailscaled.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS TARGETARCH
RUN apk add --no-cache git
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .
COPY --from=web /w/dist ./web/dist

RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -trimpath -ldflags="-s -w" -o /papaya ./cmd/papaya

# --- runtime ---
# alpine rather than scratch: CA certificates are needed to reach the Tailscale
# control plane, and the process must not run as root.
FROM alpine:3.20
RUN apk add --no-cache ca-certificates \
 && adduser -D -u 10001 papaya \
 && mkdir -p /var/lib/papaya /etc/papaya \
 && chown -R papaya /var/lib/papaya

COPY --from=build /papaya /usr/local/bin/papaya

USER papaya

# No EXPOSE and no HEALTHCHECK on purpose. The listener lives on the tsnet
# netstack, not on a host interface, so there is no container-local port to
# publish or probe. Check liveness from the tailnet: curl http://<hostname>/healthz
ENTRYPOINT ["/usr/local/bin/papaya"]
