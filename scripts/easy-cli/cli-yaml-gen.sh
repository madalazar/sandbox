#!/bin/bash
# YAML generation functions for WFM CLI

generate_instance_yaml_from_oci() {
  local package_name="$1"
  local package_id="$2"
  local device_id="$3"
  local output_file="$4"
  local supported_deployments_raw="$5"

  local harbor_url="${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}"
  local temp_dir=$(mktemp -d)

  cd "$temp_dir"

  if ! oras pull "${harbor_url}/${OCI_ORGANIZATION}/${package_name}:latest" \
      --insecure \
      -u "${REGISTRY_USER}:${REGISTRY_PASS}" >/dev/null 2>&1; then
    echo "❌ Failed to pull package from OCI" >&2
    cd - >/dev/null
    rm -rf "$temp_dir"
    return 1
  fi

  if [ ! -f "margo.yaml" ]; then
    echo "❌ margo.yaml not found in package" >&2
    cd - >/dev/null
    rm -rf "$temp_dir"
    return 1
  fi

  echo "print extracted margo.yaml for debugging:"
  cat margo.yaml

  # Extract metadata
  local app_id=$(grep -E "^\s*id:" margo.yaml | head -1 | sed 's/.*id:\s*//' | tr -d '"' | tr -d "'" | xargs)
  local app_name=$(grep -E "^\s*name:" margo.yaml | head -1 | sed 's/.*name:\s*//' | tr -d '"' | tr -d "'" | xargs)

  local app_identifier="${app_id:-$(echo "${app_name}" | tr '[:upper:]' '[:lower:]' | tr ' ' '-' | tr -d '_.,' | sed 's/--*/-/g' | sed 's/^-//;s/-$//')}"
  app_identifier=$(echo "$app_identifier" | cut -c1-40)

  # Determine deployment type(s)
  local deployment_types=()
  mapfile -t deployment_types < <(yq eval -r '.deploymentProfiles[]?.type // empty' margo.yaml | tr -d '"' | tr -d "'" | xargs -n1)
  local deployment_type=""
  local supported_deployments=()

  if [ -n "$supported_deployments_raw" ]; then
    local normalized_supported
    normalized_supported=$(echo "$supported_deployments_raw" | tr -d '"' | tr -d "'" | tr ',' '\n' | xargs -n1)
    if [ -n "$normalized_supported" ]; then
      mapfile -t supported_deployments <<< "$normalized_supported"
    fi
  fi

  if [ ${#supported_deployments[@]} -gt 0 ] && [ ${#deployment_types[@]} -gt 0 ]; then
    for supported_type in "${supported_deployments[@]}"; do
      supported_type="${supported_type,,}"

      for available_type in "${deployment_types[@]}"; do
        if [ "${available_type,,}" = "$supported_type" ]; then
          deployment_type="$available_type"
          break 2
        fi
      done
    done
  fi

  if [ -z "$deployment_type" ]; then
    echo "No compatible deployment type match found from supported_deployments; strict compatibility enforcement will fail deployment" >&2
  fi

  echo "print supported deployment types for device: ${supported_deployments[*]}"
  echo "print extracted deployment types for debugging: ${deployment_types[*]}"
  echo "print extracted deployment type for debugging: $deployment_type"

  if [ -z "$deployment_type" ]; then
    echo "❌ No strict profile-type match between device support [${supported_deployments[*]}] and app deploymentProfiles [${deployment_types[*]}]" >&2
    cd - >/dev/null
    rm -rf "$temp_dir"
    return 1
  fi

  deployment_type="${deployment_type,,}"
  echo "print extracted deployment type for debugging: $deployment_type"

  local profile_type="$deployment_type"

  local repository=$(get_oci_repository_path "$package_name" "$temp_dir/margo.yaml")

  if [ "$profile_type" = "helm" ]; then
    if ! generate_helm_instance "$app_identifier" "$package_id" "$device_id" "$repository" "$output_file" "$temp_dir/margo.yaml" "$profile_type"; then
      echo "❌ Failed to generate helm deployment instance YAML" >&2
      cd - >/dev/null
      rm -rf "$temp_dir"
      return 1
    fi
  elif [ "$profile_type" = "compose" ]; then
    if ! generate_compose_instance "$app_identifier" "$package_id" "$device_id" "$repository" "$output_file" "$temp_dir/margo.yaml" "$profile_type"; then
      echo "❌ Failed to generate compose deployment instance YAML" >&2
      cd - >/dev/null
      rm -rf "$temp_dir"
      return 1
    fi
  else
    echo "❌ Unsupported deployment type: $profile_type" >&2
    cd - >/dev/null
    rm -rf "$temp_dir"
    return 1
  fi

  echo "Generated instance.yaml for package '$package_name' at: $output_file"
  cat "$output_file"  # Print the generated instance.yaml for debugging

  cd - >/dev/null
  rm -rf "$temp_dir"
  return 0
}

append_component_parameters() {
  local margo_file="$1"
  local output_file="$2"
  local components_csv="$3"

  if ! yq eval 'true' "$margo_file" >/dev/null 2>&1; then
    echo "❌ yq failed to parse margo.yaml" >&2
    return 1
  fi

  local has_parameters
  if ! has_parameters=$(yq eval -r 'has("parameters") and (.parameters != null) and ((.parameters | type) == "!!map") and ((.parameters | length) > 0)' "$margo_file"); then
    echo "❌ Failed to detect parameters in margo.yaml with yq" >&2
    return 1
  fi

  if [ "$has_parameters" != "true" ]; then
    return 0
  fi

  local -A component_lookup=()
  local parsed_component=""
  IFS=',' read -r -a parsed_components <<< "$components_csv"
  for parsed_component in "${parsed_components[@]}"; do
    if [ -n "$parsed_component" ]; then
      component_lookup["$parsed_component"]=1
    fi
  done

  local parameter_names
  if ! parameter_names=$(yq eval '(.parameters // {} | keys | .[])' "$margo_file"); then
    echo "❌ Failed to read parameters from margo.yaml with yq (requires yq v4)" >&2
    return 1
  fi

  local wrote_parameters=false
  while IFS= read -r parameter_name; do
    if [ -z "$parameter_name" ] || [ "$parameter_name" = "null" ]; then
      continue
    fi

    local target_components
    if ! target_components=$(PARAM_NAME="$parameter_name" yq eval -r '.parameters[env(PARAM_NAME)].targets[]?.components[]?' "$margo_file"); then
      echo "❌ Failed to read parameter targets for '$parameter_name'" >&2
      return 1
    fi

    local include_parameter=false
    while IFS= read -r target_component; do
      if [ -n "$target_component" ] && [ "$target_component" != "null" ] && [ -n "${component_lookup[$target_component]:-}" ]; then
        include_parameter=true
        break
      fi
    done <<< "$target_components"

    if [ "$include_parameter" = true ]; then
      if [ "$wrote_parameters" = false ]; then
        echo "  parameters:" >> "$output_file"
        wrote_parameters=true
      fi

      if ! (
        set -o pipefail
        PARAM_NAME="$parameter_name" COMPONENTS_CSV="$components_csv" yq eval '{
          (env(PARAM_NAME)): (
            .parameters[env(PARAM_NAME)]
            | .targets |= map(
                .components |= map(. as $c | select(((env(COMPONENTS_CSV) | split(",") | map(select(. == $c)) | length) > 0)))
                | select((.components | length) > 0)
              )
          )
        }' "$margo_file" | sed 's/^/    /' >> "$output_file"
      ); then
        echo "❌ Failed to render filtered parameter '$parameter_name'" >&2
        return 1
      fi
    fi
  done <<< "$parameter_names"

  return 0
}

extract_profile_component_kv() {
  local margo_file="$1"
  local target_profile_type="$2"
  local profile_kind="$3"

  if ! yq eval 'true' "$margo_file" >/dev/null 2>&1; then
    echo "❌ yq failed to parse margo.yaml" >&2
    return 1
  fi

  case "$profile_kind" in
    helm)
      PROFILE_WANTED="$target_profile_type" yq eval -r '
        ((.deploymentProfiles // [])
          | map(select((.type // "") == strenv(PROFILE_WANTED)))
          | .[0].components // [])[] as $c
        | "COMPONENT_NAME:" + ($c.name // "")
          + "\nREPOSITORY:" + ($c.properties.repository // $c.repository // "")
          + "\nREVISION:" + (($c.properties.revision // $c.revision // "0.1.0") | tostring)
      ' "$margo_file"
      ;;
    compose)
      PROFILE_WANTED="$target_profile_type" yq eval -r '
        ((.deploymentProfiles // [])
          | map(select((.type // "") == strenv(PROFILE_WANTED)))
          | .[0].components // [])[] as $c
        | "COMPONENT_NAME:" + ($c.name // "")
          + "\nPACKAGE_LOCATION:" + ($c.properties.packageLocation // $c.packageLocation // "")
      ' "$margo_file"
      ;;
    *)
      echo "❌ Unsupported profile kind '$profile_kind' in extract_profile_component_kv (expected: helm or compose)" >&2
      return 1
      ;;
  esac
}

generate_helm_instance() {
  local app_identifier="$1"
  local package_id="$2"
  local device_id="$3"
  local repository="$4"
  local output_file="$5"
  local margo_file="$6"
  local target_profile_type="${7:-helm}"
  local -a selected_profile_components=()

  local instance_name=$(echo "${app_identifier}-instance" | cut -c1-53)

  cat > "$output_file" <<EOF
# This is an input template allowing the WFM user to modify deployment instance specific parameters(currently read-only).
# This file is not MARGO specified, however these parameters will be used to create the MARGO ApplicationDeployment

apiVersion: non-margo.org
kind: ApplicationDeployment
metadata:
  name: ${instance_name}
spec:
  appPackageRef:
    id: ${package_id}
  deviceRef:
    id: ${device_id}
  deploymentProfile:
    type: helm
    components:
EOF

  if grep -q "components:" "$margo_file"; then
    local current_name=""
    local current_repo=""
    local current_rev="0.1.0"
    local profile_components_kv

    if ! profile_components_kv=$(extract_profile_component_kv "$margo_file" "$target_profile_type" "helm"); then
      echo "❌ Failed to extract helm profile components from margo.yaml" >&2
      return 1
    fi

    while IFS=: read -r key value; do
      case "$key" in
        COMPONENT_NAME)
          current_name="$value"
          ;;
        REPOSITORY)
          current_repo="$value"
          ;;
        REVISION)
          current_rev="$value"
          if [ -n "$current_name" ] && [ -n "$current_repo" ]; then
            selected_profile_components+=("$current_name")
            cat >> "$output_file" <<COMPONENT
      - name: ${current_name}
        properties:
          repository: ${current_repo}
          revision: ${current_rev}
          wait: true
          timeout: 5m
COMPONENT
            current_name=""
            current_repo=""
            current_rev="0.1.0"
          fi
          ;;
      esac
    done <<< "$profile_components_kv"
  else
    local component_name=$(echo "$app_identifier" | cut -c1-40)
    local chart_version=$(grep -E "^\s*version:" "$margo_file" | head -1 | sed 's/.*version:\s*//' | tr -d '"' | tr -d "'" | xargs)
    chart_version="${chart_version:-0.1.0}"
    selected_profile_components+=("$component_name")

    cat >> "$output_file" <<COMPONENT
      - name: ${component_name}
        properties:
          repository: ${repository}
          revision: ${chart_version}
          wait: true
          timeout: 5m
COMPONENT
  fi

  local helm_components_csv=""
  if [ ${#selected_profile_components[@]} -gt 0 ]; then
    helm_components_csv=$(IFS=,; echo "${selected_profile_components[*]}")
  fi

  if ! append_component_parameters "$margo_file" "$output_file" "$helm_components_csv"; then
    return 1
  fi
}

generate_compose_instance() {
  local app_identifier="$1"
  local package_id="$2"
  local device_id="$3"
  local repository="$4"
  local output_file="$5"
  local margo_file="$6"
  local target_profile_type="${7:-compose}"

  local instance_name=$(echo "${app_identifier}-instance" | cut -c1-53)
  local stack_name=$(echo "${app_identifier}-stack" | cut -c1-40)
  local -a selected_profile_components=()

  cat > "$output_file" <<EOF
# This is an input template allowing the WFM user to modify deployment instance specific parameters(currently read-only).
# This file is not MARGO specified, however these parameters will be used to create the MARGO ApplicationDeployment
apiVersion: non-margo.org
kind: ApplicationDeployment
metadata:
  name: ${instance_name}
spec:
  appPackageRef:
    id: ${package_id}
  deviceRef:
    id: ${device_id}
  deploymentProfile:
    type: compose
    components:
EOF

  if grep -q "components:" "$margo_file"; then
    local current_name=""
    local profile_components_kv

    if ! profile_components_kv=$(extract_profile_component_kv "$margo_file" "$target_profile_type" "compose"); then
      echo "❌ Failed to extract compose profile components from margo.yaml" >&2
      return 1
    fi

    while IFS=: read -r key value; do
      case "$key" in
        COMPONENT_NAME)
          current_name="$value"
          if [ -n "$current_name" ]; then
            selected_profile_components+=("$current_name")
          fi
          ;;
        PACKAGE_LOCATION)
          if [ -n "$current_name" ]; then
            cat >> "$output_file" <<COMPONENT
      - name: ${current_name}
        properties:
          packageLocation: ${value}
COMPONENT
            current_name=""
          fi
          ;;
      esac
    done <<< "$profile_components_kv"
  else
    cat >> "$output_file" <<COMPONENT
      - name: ${stack_name}
        properties:
          packageLocation: ${repository}
COMPONENT
    selected_profile_components+=("$stack_name")
  fi

  local compose_components_csv=""
  if [ ${#selected_profile_components[@]} -gt 0 ]; then
    compose_components_csv=$(IFS=,; echo "${selected_profile_components[*]}")
  fi

  if ! append_component_parameters "$margo_file" "$output_file" "$compose_components_csv"; then
    return 1
  fi
}

generate_wfm_package_yaml() {
  local package_repo="$1"
  local output_file="$2"
  local harbor_url="${EXPOSED_HARBOR_HOST}:${EXPOSED_HARBOR_PORT}"

  cat > "$output_file" <<EOF
# This is an input template allowing the WFM user to modify deployment instance specific parameters.
# This file is not MARGO specified, however these parameters will be used to create the MARGO ApplicationDeployment
apiVersion: non-margo.org
kind: ApplicationPackage
metadata:
  name: ${package_repo}
  labels:
    env: dev
  annotations:
    description: "Application package from Harbor OCI Registry"
spec:
  sourceType: OCI_REPO
  source:
    registryUrl: "https://${harbor_url}"
    repository: "${OCI_ORGANIZATION}/${package_repo}"
    tag: "latest"
    authentication:
      type: "basic"
      username: "${REGISTRY_USER}"
      password: "${REGISTRY_PASS}"
EOF
}


# Legacy template-based functions (for backward compatibility)
get_instance_file_path() {
  local package_name="$1"

  if [ -z "$HOME" ]; then
    echo "❌ HOME environment variable not set" >&2
    return 1
  fi

  local template_base="$HOME/symphony/cli/templates/margo"

  case $package_name in
    "custom-otel-helm-app-package"|"custom-otel-helm-app"|"custom-otel"|"otel-demo-pkg")
      original_file_path="$template_base/custom-otel-helm/instance.yaml"
      file_path="$template_base/custom-otel-helm/instance.yaml.copy"
      ;;
    "nextcloud-compose-app-package"|"nextcloud-compose-app"|"nextcloud"|"nextcloud-pkg")
      original_file_path="$template_base/nextcloud-compose/instance.yaml"
      file_path="$template_base/nextcloud-compose/instance.yaml.copy"
      ;;
    *)
      local search_name="${package_name%-app-package}"
      local template_dir=$(find "$template_base" -maxdepth 1 -type d -iname "*${search_name}*" 2>/dev/null | head -1)

      if [ -n "$template_dir" ] && [ -f "$template_dir/instance.yaml" ]; then
        original_file_path="$template_dir/instance.yaml"
        file_path="$template_dir/instance.yaml.copy"
      else
        echo "❌ No instance template found for package '$package_name'" >&2
        echo "ℹ️  Searched in: $template_base" >&2
        return 1
      fi
      ;;
  esac

  if [ -f "$original_file_path" ]; then
    cp -f "$original_file_path" "$file_path"
    echo "$file_path"
  else
    echo "❌ Deployment file not found: $original_file_path" >&2
    return 1
  fi
}
