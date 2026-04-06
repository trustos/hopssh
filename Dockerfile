# Multi-stage build for hopssh.
#
# Usage:
#   docker build -t hopssh .
#   docker run -p 8080:8080 -v hopssh-data:/data hopssh

# --- Frontend build stage ---
FROM node:20-slim AS frontend

WORKDIR /frontend
COPY frontend/package*.json ./
RUN npm ci
COPY frontend/ .
RUN npm run build

# --- Go build stage ---
FROM golang:1.24-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends patch && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
COPY patches/ patches/
COPY Makefile ./

RUN go mod download
COPY . .
RUN make vendor

# Copy built frontend into embed location.
COPY --from=frontend /frontend/build ./internal/frontend/dist/

# Build static binaries.
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/hop-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux go build -mod=vendor -trimpath -ldflags='-s -w' -o /out/hop-agent ./cmd/agent

# --- Runtime stage ---
FROM debian:bookworm-slim

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates && \
    rm -rf /var/lib/apt/lists/* && \
    useradd -r -s /bin/false hopssh && \
    mkdir -p /data && chown hopssh:hopssh /data

COPY --from=builder /out/hop-server /usr/local/bin/hop-server
COPY --from=builder /out/hop-agent /usr/local/bin/hop-agent

VOLUME /data
EXPOSE 8080

USER hopssh

ENTRYPOINT ["hop-server"]
CMD ["--addr", ":8080", "--data", "/data", "--endpoint", "http://localhost:8080"]
