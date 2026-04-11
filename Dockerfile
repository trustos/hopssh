# Multi-stage build for hopssh control plane.
#
# Usage:
#   docker build -t hopssh .
#   docker run -p 9473:9473 -p 42001-42100:42001-42100/udp -v hopssh-data:/data -e HOPSSH_ENDPOINT=http://YOUR_IP:9473 hopssh

# --- Frontend build stage ---
FROM node:22-slim AS frontend
WORKDIR /frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# --- Go build stage ---
FROM golang:1.25-bookworm AS builder
RUN apt-get update && apt-get install -y --no-install-recommends patch && rm -rf /var/lib/apt/lists/*
WORKDIR /src
COPY . .
RUN make setup
COPY --from=frontend /frontend/build ./internal/frontend/dist/

ARG VERSION=dev
ARG COMMIT=unknown
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath \
    -ldflags="-s -w -X github.com/trustos/hopssh/internal/buildinfo.Version=${VERSION} -X github.com/trustos/hopssh/internal/buildinfo.Commit=${COMMIT}" \
    -o /out/hop-server ./cmd/server

# --- Runtime stage (distroless, no shell, minimal attack surface) ---
FROM gcr.io/distroless/base-debian12:nonroot

COPY --from=builder /out/hop-server /hop-server

VOLUME /data

# API + dashboard
EXPOSE 9473/tcp
# Nebula lighthouse (one per network, starting at 42001)
EXPOSE 42001-42100/udp
# DNS server (one per network, starting at 15300)
EXPOSE 15300-15400/udp

HEALTHCHECK --interval=10s --timeout=5s --retries=3 \
    CMD ["/hop-server", "healthz"]

ENTRYPOINT ["/hop-server"]
CMD ["--addr", ":9473", "--data", "/data"]
