#!/usr/bin/env bash
set -Eeuo pipefail

usage() {
  cat <<'EOF'
Usage: test-local-override.sh [options]

Builds the local workload-fleet-management-client image, restarts the device
agent stack, and validates container/log health.

Options:
  --skip-wfm-cert-copy        Skip copying ca-cert.pem from WFM.
  --device-type <name>        Device type passed to device-agent.sh (default: docker).
  -h, --help                  Show this help text.

Defaults:
  sandbox-root: two levels above this script
  copy-wfm-certs: enabled
  cert-source: labrat@10.123.232.166:/home/labrat/symphony/api/certificates/ca-cert.pem
  cert-dest:   $HOME/certs
EOF
}

log() {
  printf '[override-test] %s\n' "$*"
}

run_cmd() {
  log "Running: $*"
  "$@"
}

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SANDBOX_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
COPY_WFM_CERTS=true
CERT_SOURCE="labrat@10.123.232.166:/home/labrat/symphony/api/certificates/ca-cert.pem"
CERT_DEST="${HOME}/certs"
DEVICE_TYPE="docker"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --skip-wfm-cert-copy)
      COPY_WFM_CERTS=false
      shift
      ;;
    --device-type)
      DEVICE_TYPE="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "Unknown argument: $1" >&2
      usage
      exit 1
      ;;
  esac
done

SCRIPTS_DIR="$SANDBOX_ROOT/scripts"
ENV_FILE="$SCRIPTS_DIR/device-agent.env"
DEVICE_AGENT_SCRIPT="$SCRIPTS_DIR/device-agent.sh"
DOCKERFILE_PATH="$SANDBOX_ROOT/poc/device/agent/Dockerfile"
STANDARD_GENERATE_SCRIPT="$SANDBOX_ROOT/standard/generate.sh"
LOCAL_SPEC_URL="/home/labrat/temp-workspace/specification/system-design/specification/margo-management-interface/workload-management-api-1.0.0.yaml"
IMAGE_NAME="margo.org/workload-fleet-management-client:latest"
SUCCESS_MESSAGE="Workload Fleet Management Client started successfully"
GENERATE_SCRIPT_BACKUP=""

restore_generate_script() {
  if [[ -n "${GENERATE_SCRIPT_BACKUP:-}" && -f "$GENERATE_SCRIPT_BACKUP" ]]; then
    cp "$GENERATE_SCRIPT_BACKUP" "$STANDARD_GENERATE_SCRIPT"
    rm -f "$GENERATE_SCRIPT_BACKUP"
    log "Restored $STANDARD_GENERATE_SCRIPT"
  fi
}

patch_generate_script_spec() {
  if [[ ! -f "$STANDARD_GENERATE_SCRIPT" ]]; then
    echo "generate.sh not found: $STANDARD_GENERATE_SCRIPT" >&2
    exit 1
  fi

  if [[ ! -f "$LOCAL_SPEC_URL" ]]; then
    echo "Local spec not found: $LOCAL_SPEC_URL" >&2
    exit 1
  fi

  GENERATE_SCRIPT_BACKUP="$(mktemp)"
  cp "$STANDARD_GENERATE_SCRIPT" "$GENERATE_SCRIPT_BACKUP"

  if grep -q '^SPEC_URL=' "$STANDARD_GENERATE_SCRIPT"; then
    sed -i "s|^SPEC_URL=.*|SPEC_URL=\"$LOCAL_SPEC_URL\"|" "$STANDARD_GENERATE_SCRIPT"
    log "Patched SPEC_URL in $STANDARD_GENERATE_SCRIPT"
    return
  fi

  if grep -q '^WFM_SBI_SPEC=' "$STANDARD_GENERATE_SCRIPT"; then
    sed -i "s|^WFM_SBI_SPEC=.*|WFM_SBI_SPEC=\"$LOCAL_SPEC_URL\"|" "$STANDARD_GENERATE_SCRIPT"
    log "Patched WFM_SBI_SPEC in $STANDARD_GENERATE_SCRIPT (SPEC_URL not present)"
    return
  fi

  echo "Neither SPEC_URL nor WFM_SBI_SPEC found in $STANDARD_GENERATE_SCRIPT" >&2
  exit 1
}

if [[ ! -f "$DOCKERFILE_PATH" ]]; then
  echo "Dockerfile not found: $DOCKERFILE_PATH" >&2
  exit 1
fi

if [[ ! -f "$ENV_FILE" ]]; then
  echo "Environment file not found: $ENV_FILE" >&2
  exit 1
fi

if [[ ! -f "$DEVICE_AGENT_SCRIPT" ]]; then
  echo "device-agent.sh not found: $DEVICE_AGENT_SCRIPT" >&2
  exit 1
fi

trap restore_generate_script EXIT
log "Patching standard generate script to use local specification"
patch_generate_script_spec

log "Building local override image"
run_cmd docker build -f "$DOCKERFILE_PATH" -t "$IMAGE_NAME" "$SANDBOX_ROOT"

log "Loading environment variables from $ENV_FILE"
set -a
# shellcheck source=/dev/null
source "$ENV_FILE"
set +a

log "Stopping existing device agent container"
run_cmd sudo -E bash "$DEVICE_AGENT_SCRIPT" "$DEVICE_TYPE" stop-docker

if [[ "$COPY_WFM_CERTS" == "true" ]]; then
  log "Copying WFM CA cert to $CERT_DEST"
  if [[ ! -d "$CERT_DEST" ]]; then
    echo "Cert destination directory does not exist: $CERT_DEST" >&2
    exit 1
  fi
  run_cmd sudo scp "$CERT_SOURCE" "$CERT_DEST/"
else
  log "Skipping WFM cert copy (--skip-wfm-cert-copy)"
fi

log "Generating RSA certs"
run_cmd sudo -E bash "$DEVICE_AGENT_SCRIPT" "$DEVICE_TYPE" create-rsa-certs

log "Generating ECDSA certs"
run_cmd sudo -E bash "$DEVICE_AGENT_SCRIPT" "$DEVICE_TYPE" create-ecdsa-certs

log "Starting device agent container"
run_cmd sudo -E bash "$DEVICE_AGENT_SCRIPT" "$DEVICE_TYPE" start-docker

log "Checking container presence"
PS_OUTPUT="$(docker ps -a --format '{{.Names}}\t{{.Image}}\t{{.Status}}' | grep 'workload-fleet-management-client' || true)"
if [[ -z "$PS_OUTPUT" ]]; then
  echo "No workload-fleet-management-client container entry found in docker ps -a" >&2
  exit 1
fi
printf '%s\n' "$PS_OUTPUT"

CONTAINER_NAME="$(printf '%s\n' "$PS_OUTPUT" | head -n1 | awk '{print $1}')"
if [[ -z "$CONTAINER_NAME" ]]; then
  echo "Unable to determine container name from docker ps output" >&2
  exit 1
fi

log "Waiting for the container to initialize (up to 30 seconds)..."
sleep 30

log "Checking logs for successful startup and obvious errors"
LOG_OUTPUT="$(docker logs "$CONTAINER_NAME" 2>&1 || true)"

if ! grep "$SUCCESS_MESSAGE" <<<"$LOG_OUTPUT"; then
  echo "Startup success message not found in container logs" >&2
  echo "Expected: $SUCCESS_MESSAGE" >&2
  exit 1
fi

log "Local override test completed successfully"
log "Container: $CONTAINER_NAME"
