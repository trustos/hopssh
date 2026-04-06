# Multi-stage build for hopssh control plane server.
#
# Usage:
#   docker build -t hopssh .
#   docker run -p 8080:8080 -v hopssh-data:/data hopssh server
#   docker run -p 8080:8080 -v hopssh-data:/data hopssh server --trusted-proxy

# --- Build stage ---
FROM golang:1.24-bookworm AS builder

RUN apt-get update && apt-get install -y --no-install-recommends patch && rm -rf /var/lib/apt/lists/*

WORKDIR /src
COPY go.mod go.sum ./
COPY patches/ patches/
COPY Makefile ./

# Vendor + patch (cached unless go.mod/patches change).
RUN go mod download
COPY . .
RUN make vendor

# Build both binaries (static, no CGO — modernc.org/sqlite is pure Go).
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
