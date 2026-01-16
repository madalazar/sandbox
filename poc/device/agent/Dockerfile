# Stage 1: Build
FROM golang:1.24.4 AS builder

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a static binary

RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-X main.version=tobedone" -o device-agent ./poc/device/agent

# Stage 2: Get Docker binary and compose plugin from official image
FROM docker:28.3.3-cli AS docker-source

# Stage 3: Runtime
FROM alpine:3.18

# Install CA certificates and other essentials
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /

COPY --from=builder /app/device-agent .
COPY --from=builder /app/poc/device/agent/config ./config
COPY --from=docker-source /usr/local/bin/docker /usr/bin/docker

# Create the cli-plugins directory FIRST
RUN mkdir -p /usr/local/libexec/docker/cli-plugins

# Copy Docker Compose plugin (check if it exists in the source)
COPY --from=docker-source /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose

# Make the plugin executable
RUN chmod +x /usr/local/libexec/docker/cli-plugins/docker-compose

# Create volume for config override
VOLUME ["/config"]

# Add metadata labels
LABEL description="Margo Device Workload Fleet management Client for edge workload management" \
      source="https://github.com/margo/sandbox" \
      title="Device Workload Fleet management Client"

# Use ENTRYPOINT for better flexibility
ENTRYPOINT ["./device-agent"]
CMD ["-config", "config/config.yaml"]
