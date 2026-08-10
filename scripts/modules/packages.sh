#!/bin/bash
# modules/packages.sh - OCI package management

source "$(dirname "${BASH_SOURCE[0]}")/../lib/common.sh"

push_app_package_to_registry() {
  local package_source_dir="$1"
  local package_repo_name="$2"
  local tag="${3:-latest}"

  if [ -z "$package_source_dir" ] || [ -z "$package_repo_name" ]; then
    echo "❌ Usage: push_app_package_to_registry <package_source_dir> <package_repo_name> [tag]"
    echo "   Example: push_app_package_to_registry '$HOME/sandbox/poc/tests/artefacts/nextcloud-compose' 'nextcloud-compose-app-package'"
    return 1
  fi

  local app_dir="$package_source_dir/margo-package"
  local repository="${OCI_ORGANIZATION}/${package_repo_name}"
  local registry_host="${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}"
  local original_dir

  original_dir="$(pwd)"
  cd "$app_dir" || { echo "❌ margo-package dir missing: $app_dir"; return 1; }

  echo "📦 Pushing ${package_repo_name} package to OCI Registry (HTTPS)..."
  echo "$REGISTRY_PASS" | oras login "$registry_host" \
    -u "$REGISTRY_USER" --password-stdin --insecure

  if [ ! -f "margo.yaml" ]; then
    cd "$original_dir" || true
    echo "❌ margo.yaml not found in $app_dir"
    return 1
  fi

  local files=("margo.yaml:application/vnd.margo.app.description.v1+yaml")
  if [ -d "resources" ] && [ "$(ls -A resources 2>/dev/null)" ]; then
    while IFS= read -r file; do
      if [ -f "$file" ]; then
        files+=("$file:application/octet-stream")
      fi
    done < <(find resources -type f 2>/dev/null)
  fi

  echo "Pushing files: ${files[*]}"
  oras push "${registry_host}/${repository}:${tag}" \
    --artifact-type "application/vnd.margo.app.v1+json" \
    --insecure \
    "${files[@]}"

  local push_status=$?
  cd "$original_dir" || true

  if [ $push_status -eq 0 ]; then
    echo "✅ ${package_repo_name} package pushed to OCI Registry (HTTPS)"
    echo "📍 Location: https://${registry_host}/${repository}:${tag}"
  else
    echo "❌ Failed to push ${package_repo_name} package"
    return 1
  fi
}

push_nextcloud_to_oci() {
  push_app_package_to_registry \
    "$HOME/sandbox/poc/tests/artefacts/nextcloud-compose" \
    "nextcloud-compose-app-package"
}

push_custom_otel_to_oci() {
  push_app_package_to_registry \
    "$HOME/sandbox/poc/tests/artefacts/custom-otel-helm-app" \
    "custom-otel-helm-app-package"
}

build_custom_otel_container_images() {
  echo "Building/Downloading Custom Otel images..."

  cd "$HOME/sandbox/poc/tests/artefacts/custom-otel-helm-app/code/app"
  docker build . -t "${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}/library/custom-otel-app:latest"
  
  echo "Ensuring Harbor registry login (HTTPS)..."
  docker login "${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}" -u admin -p Harbor12345
  echo "Pushing otel images to Harbor..."
  docker push "${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}/library/custom-otel-app:latest"

  OTEL_APP_CONTAINER_URL="${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}/library/custom-otel-app"
  deploy_file="$HOME/sandbox/poc/tests/artefacts/custom-otel-helm-app/code/helm/values.yaml"
  tag="latest"

  echo "Preparing Helm chart..."
  cd "$HOME/sandbox/poc/tests/artefacts/custom-otel-helm-app/code"
  CHART_FILE="$HOME/sandbox/poc/tests/artefacts/custom-otel-helm-app/code/helm/Chart.yaml"
  CHART_VERSION=$(grep "^version:" "$CHART_FILE" | awk '{print $2}')

  echo "Using existing chart version: $CHART_VERSION"
                                       
  sed -i "s|{{REPOSITORY}}|$OTEL_APP_CONTAINER_URL|g" "$deploy_file" 2>/dev/null || true
  sed -i "s|{{TAG}}|$tag|g" "$deploy_file" 2>/dev/null || true
  echo "Packaging Helm chart version $CHART_VERSION..."
  helm package helm/

  echo "Pushing chart to Harbor (HTTPS)..."
  helm push "custom-otel-helm-${CHART_VERSION}.tgz" "oci://${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}/library" --insecure-skip-tls-verify

                                                            
  HELM_REPOSITORY="oci://${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}/library/custom-otel-helm"
  HELM_REVISION="$CHART_VERSION"
  helm_deploy_file="$HOME/sandbox/poc/tests/artefacts/custom-otel-helm-app/margo-package/margo.yaml"

  echo "Updating margo.yaml with chart version $CHART_VERSION..."
  sed -i "s|{{HELM_REPOSITORY}}|$HELM_REPOSITORY|g" "$helm_deploy_file" 2>/dev/null || true
  sed -i "s|{{HELM_REVISION}}|$HELM_REVISION|g" "$helm_deploy_file" 2>/dev/null || true

  echo "✅ Custom otel chart version $CHART_VERSION successfully pushed to Harbor (HTTPS)"
  echo "📦 Chart: oci://${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}/library/custom-otel-helm:$CHART_VERSION"
}
