# Nomad job spec for hopssh control plane.
# Adjust image, endpoint, and volume configuration for your environment.
#
# Usage:
#   nomad job run deploy/nomad.hcl

job "hopssh" {
  datacenters = ["dc1"]
  type        = "service"

  group "control-plane" {
    count = 1 # SQLite is single-writer — do not scale beyond 1

    network {
      port "api" {
        static = 9473
        to     = 9473
      }
      # Nebula lighthouse ports (UDP, one per network)
      port "nebula" {
        static = 42001
        to     = 42001
      }
    }

    volume "data" {
      type      = "host"
      source    = "hopssh-data"
      read_only = false
    }

    task "hop-server" {
      driver = "docker"

      config {
        image = "ghcr.io/trustos/hopssh:latest"
        ports = ["api", "nebula"]

        # Expose full UDP port ranges for lighthouse + DNS
        port_map {
          api    = 9473
          nebula = 42001
        }
      }

      volume_mount {
        volume      = "data"
        destination = "/data"
        read_only   = false
      }

      env {
        HOPSSH_ENDPOINT = "https://hopssh.example.com"
        HOPSSH_ADDR     = ":9473"
        HOPSSH_DATA     = "/data"
        # HOPSSH_TRUSTED_PROXY    = "true"  # Enable if behind a reverse proxy
        # HOPSSH_ALLOWED_ORIGINS  = "https://hopssh.example.com"
        # HOPSSH_ENCRYPTION_KEY   = "..."   # Set via Vault or Nomad Variables
      }

      resources {
        cpu    = 500
        memory = 512
      }

      service {
        name = "hopssh"
        port = "api"

        check {
          type     = "http"
          path     = "/healthz"
          interval = "10s"
          timeout  = "5s"
        }
      }

      # Allow graceful shutdown (10s server drain + 5s buffer)
      kill_timeout = "15s"
    }
  }
}
