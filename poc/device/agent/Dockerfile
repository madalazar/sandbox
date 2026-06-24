# Stage 1: Build for correct OS Architecture
FROM --platform=$BUILDPLATFORM golang:1.25.10 AS builder

ARG VERSION
ARG COMMIT
ARG DATE

ARG TARGETOS
ARG TARGETARCH


WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Build a static binary
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath \
    -ldflags="-X main.build=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}" \
    -o device-agent ./poc/device/agent


    
# Stage 2: Get Docker binary and compose plugin from official image
# FROM docker:28.3.3-cli AS docker-source
FROM --platform=$BUILDPLATFORM docker:28.3.3-cli AS docker-source

# Stage 3: Build pqos for Alpine runtime
FROM --platform=$BUILDPLATFORM alpine:3.18 AS pqos-builder

ARG INTEL_CMT_CAT_REF=v4.4.1

RUN apk add --no-cache git build-base cmake linux-headers

WORKDIR /src/intel-cmt-cat
RUN git clone --depth 1 --branch ${INTEL_CMT_CAT_REF} https://github.com/intel/intel-cmt-cat.git . \
    && make -C lib -j"$(nproc)" \
    && make -C pqos -j"$(nproc)" \
    && install -D pqos/pqos /out/usr/local/bin/pqos \
    && install -D lib/libpqos.so /out/usr/local/lib/libpqos.so \
    && install -D lib/libpqos.so.4 /out/usr/local/lib/libpqos.so.4 \
    && install -D lib/libpqos.so.4.4.1 /out/usr/local/lib/libpqos.so.4.4.1

# Stage 3: Runtime
FROM alpine:3.18

ARG TARGETARCH

# Install runtime tools required by deployment logic.
# util-linux + kmod are needed for nsenter/modprobe, pqos is copied from pqos-builder.
RUN apk add --no-cache ca-certificates tzdata yq curl util-linux kmod \
    && KUBECTL_VERSION="v1.31.0" \
    && case "$TARGETARCH" in \
        amd64) KUBECTL_ARCH="amd64" ;; \
        arm64) KUBECTL_ARCH="arm64" ;; \
        *) echo "Unsupported TARGETARCH: $TARGETARCH" && exit 1 ;; \
       esac \
    && curl -fsSL "https://dl.k8s.io/release/${KUBECTL_VERSION}/bin/linux/${KUBECTL_ARCH}/kubectl" -o /usr/local/bin/kubectl \
    && chmod +x /usr/local/bin/kubectl

WORKDIR /

COPY --from=builder /app/device-agent .
COPY --from=builder /app/poc/device/agent/config ./config
COPY --from=pqos-builder /out/usr/local/bin/pqos /usr/local/bin/pqos
COPY --from=pqos-builder /out/usr/local/lib/libpqos.so /usr/local/lib/libpqos.so
COPY --from=pqos-builder /out/usr/local/lib/libpqos.so.4 /usr/local/lib/libpqos.so.4
COPY --from=pqos-builder /out/usr/local/lib/libpqos.so.4.4.1 /usr/local/lib/libpqos.so.4.4.1
# this will remove the dependency from docker image 
COPY --from=docker-source /usr/local/bin/docker /usr/bin/docker

# Create the cli-plugins directory FIRST
RUN mkdir -p /usr/local/libexec/docker/cli-plugins

# Copy Docker Compose plugin (check if it exists in the source)
COPY --from=docker-source /usr/local/libexec/docker/cli-plugins/docker-compose /usr/local/libexec/docker/cli-plugins/docker-compose

# Make the plugin executable
RUN chmod +x /usr/local/libexec/docker/cli-plugins/docker-compose

ENV LD_LIBRARY_PATH=/usr/local/lib

# Create volume for config override
VOLUME ["/config"]

# Use ENTRYPOINT for better flexibility
ENTRYPOINT ["./device-agent"]
CMD ["-config", "config/config.yaml"]
