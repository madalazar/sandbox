#!/bin/bash
# modules/nri.sh - NRI Balloon Resource Policy plugin management
#
# Functions:
#   generate_balloon_policy   - Build balloons values YAML from margo-package/margo.yaml + host topology
#   install_balloon_nri_plugin  - Helm-install the NRI balloon plugin
#   update_balloon_nri_plugin   - Helm-upgrade the NRI balloon plugin
#   uninstall_balloon_nri_plugin - Helm-uninstall the NRI balloon plugin
#
# Prerequisites:
#   - k3s environment with a running device agent
#   - capabilities.sh helpers must be sourced (or this file self-sources them)

SCRIPT_DIR_NRI="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR_NRI}/cpu-topology.sh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_nri_compact_cpuset() {
  local -a ids=("$@")
  local n="${#ids[@]}"
  [[ "$n" -eq 0 ]] && { echo ""; return; }

  local start="${ids[0]}"
  local prev="${ids[0]}"
  local out=""
  local i curr

  for ((i = 1; i < n; i++)); do
    curr="${ids[i]}"
    if (( curr == prev + 1 )); then
      prev="$curr"
      continue
    fi

    [[ -n "$out" ]] && out+=","
    if (( start == prev )); then
      out+="$start"
    else
      out+="${start}-${prev}"
    fi

    start="$curr"
    prev="$curr"
  done

  [[ -n "$out" ]] && out+=","
  if (( start == prev )); then
    out+="$start"
  else
    out+="${start}-${prev}"
  fi

  echo "$out"
}

_nri_resolve_margo_yamls() {
  local app_paths_csv="$1"

  if [[ -z "$app_paths_csv" ]]; then
    echo "[ERROR] at least one margo app path is required." >&2
    echo "        Expected path(s) containing margo-package/margo.yaml" >&2
    return 1
  fi

  local -a app_paths=()
  IFS=',' read -r -a app_paths <<< "$app_paths_csv"

  local app_path
  for app_path in "${app_paths[@]}"; do
    # Trim leading/trailing whitespace for each comma-separated entry.
    app_path="${app_path#"${app_path%%[![:space:]]*}"}"
    app_path="${app_path%"${app_path##*[![:space:]]}"}"
    [[ -z "$app_path" ]] && continue

    local -a candidates=(
      "$app_path/margo-package/margo.yaml"
      "$app_path/margo.yaml"
    )

    local candidate found=""
    for candidate in "${candidates[@]}"; do
      if [[ -f "$candidate" ]]; then
        found="$candidate"
        break
      fi
    done

    if [[ -z "$found" ]]; then
      echo "[ERROR] Could not find margo.yaml under '$app_path'." >&2
      echo "        Tried:" >&2
      for candidate in "${candidates[@]}"; do
        echo "          - $candidate" >&2
      done
      return 1
    fi

    echo "$found"
  done
}

_nri_extract_cpu_requirements_from_margo() {
  local margo_yaml="$1"
  local out_tsv="$2"

  if [[ ! -e "$margo_yaml" ]]; then
    echo "[ERROR] margo.yaml does not exist: $margo_yaml" >&2
    return 1
  fi

  if [[ ! -r "$margo_yaml" ]]; then
    echo "[ERROR] margo.yaml is not readable by user '$(id -un)': $margo_yaml" >&2
    echo "        Fix permissions (example):" >&2
    echo "          chmod a+r '$margo_yaml'" >&2
    echo "          chmod a+x '$(dirname "$margo_yaml")'" >&2
    return 1
  fi

  if ! command -v yq >/dev/null 2>&1; then
    echo "[ERROR] yq is required but not installed." >&2
    echo "        Install: https://github.com/mikefarah/yq#install" >&2
    return 1
  fi

  local yq_err_file
  yq_err_file="$(mktemp)"

  if ! yq -r '
    .deploymentProfiles[]? |
    select(.type == "helm.v3") |
    .requiredResources.cpu[]? |
    [(.name // ""), ((.cores // "") | tostring), (.class // ""), (.type // "")] |
    @tsv
  ' - < "$margo_yaml" > "$out_tsv" 2>"$yq_err_file"; then
    if grep -qi "permission denied" "$yq_err_file"; then
      echo "[ERROR] Permission denied while reading margo.yaml: $margo_yaml" >&2
      echo "        Ensure the current user can traverse parent directories and read the file." >&2
      sed 's/^/        yq: /' "$yq_err_file" >&2
      rm -f "$yq_err_file"
      return 1
    fi

    echo "[ERROR] Failed to parse margo.yaml: $margo_yaml" >&2
    sed 's/^/        yq: /' "$yq_err_file" >&2
    rm -f "$yq_err_file"
    return 1
  fi

  rm -f "$yq_err_file"

  if ! awk -F'\t' '
    NF != 4 { bad = 1; next }
    $1 == "" || $2 == "" || $3 == "" || $4 == "" { bad = 1; next }
    $2 !~ /^[0-9]+$/ || $2 < 1 { bad = 1; next }
    $4 != "isolated" && $4 != "shared" { bad = 1; next }
    { print }
    END { exit bad ? 1 : 0 }
  ' "$out_tsv" > "${out_tsv}.validated"; then
    echo "[ERROR] Invalid CPU requirements in $margo_yaml (name/cores/class/type)." >&2
    return 1
  fi

  mv "${out_tsv}.validated" "$out_tsv"
}

_nri_collect_existing_managed_cpus() {
  local out_file="$1"
  local exclude_release="${2:-nri-resource-policy-balloons}"
  : > "$out_file"

  if ! command -v helm >/dev/null 2>&1; then
    return 0
  fi

  if ! command -v jq >/dev/null 2>&1; then
    echo "[WARN] jq not found; skipping installed-policy CPU subtraction."
    return 0
  fi
  if ! command -v yq >/dev/null 2>&1; then
    echo "[WARN] yq not found; skipping installed-policy CPU subtraction."
    return 0
  fi

  local release_lines
  release_lines="$(helm list -A -o json 2>/dev/null | jq -r --arg ex "$exclude_release" '
    .[]
    | select((.chart // "") | startswith("nri-resource-policy-balloons"))
    | select((.name // "") != $ex)
    | select((.name // "") != "" and (.namespace // "") != "")
    | "\(.name)\t\(.namespace)"
  ' 2>/dev/null || true)"

  [[ -z "$release_lines" ]] && return 0

  local rel ns values_yaml
  while IFS=$'\t' read -r rel ns; do
    [[ -z "$rel" || -z "$ns" ]] && continue
    values_yaml="$(helm get values "$rel" -n "$ns" -a -o yaml 2>/dev/null || true)"
    [[ -z "$values_yaml" ]] && continue

    local avail_cpuset="" reserved_cpuset=""
    avail_cpuset="$(printf '%s' "$values_yaml" | yq -r '.config.availableResources.cpu // ""' 2>/dev/null || true)"
    reserved_cpuset="$(printf '%s' "$values_yaml" | yq -r '.config.reservedResources.cpu // ""' 2>/dev/null || true)"

    local avail_ranges="${avail_cpuset#cpuset:}"
    local reserved_ranges="${reserved_cpuset#cpuset:}"
    declare -A avail_map=()
    declare -A reserved_map=()
    local rid

    mark_cpu_set_from_range_list "$avail_ranges" avail_map
    mark_cpu_set_from_range_list "$reserved_ranges" reserved_map

    for rid in "${!avail_map[@]}"; do
      [[ -n "${reserved_map[$rid]:-}" ]] && continue
      echo "$rid"
    done

    printf '%s' "$values_yaml" \
      | yq -r '.config.balloonTypes[]?.preferCloseToDevices[]? // ""' 2>/dev/null \
      | sed -n 's#.*cpu\([0-9]\+\).*#\1#p'
  done <<< "$release_lines" | sort -n | uniq > "$out_file"
}

# ---------------------------------------------------------------------------
# generate_balloon_policy
#
# Builds a Helm values YAML for the nri-resource-policy-balloons chart from
# requiredResources.cpu entries in margo-package/margo.yaml.
#
# Usage: generate_balloon_policy <margo_app_path> [output_file [cache_file]]
#   margo_app_path supports a comma-separated list of paths. Each path must
#   contain margo-package/margo.yaml (or margo.yaml directly)
#   output_file defaults to $HOME/sandbox/balloon-policy.yaml
#   cache_file  defaults to $CPU_TOPOLOGY_CACHE_FILE
# ---------------------------------------------------------------------------
generate_balloon_policy() {
  local margo_app_paths="${1:-}"
  local output_file="${2:-$HOME/sandbox/balloon-policy.yaml}"
  local cache_file="${3:-$CPU_TOPOLOGY_CACHE_FILE}"

  local -a margo_yamls=()
  if ! mapfile -t margo_yamls < <(_nri_resolve_margo_yamls "$margo_app_paths"); then
    return 1
  fi
  if [[ "${#margo_yamls[@]}" -eq 0 ]]; then
    echo "[ERROR] No valid margo.yaml files resolved from input path(s)." >&2
    return 1
  fi

  mkdir -p "$(dirname "$output_file")"

  local tmp_dir req_file existing_used_file
  tmp_dir="$(mktemp -d)"
  req_file="$tmp_dir/margo-cpu-requirements.tsv"
  existing_used_file="$tmp_dir/existing-nri-cpus.txt"

  trap 'rm -rf "$tmp_dir"' RETURN

  local margo_yaml
  for margo_yaml in "${margo_yamls[@]}"; do
    local partial_req_file="$tmp_dir/req-$(basename "$(dirname "$margo_yaml")").tsv"
    if ! _nri_extract_cpu_requirements_from_margo "$margo_yaml" "$partial_req_file" >/dev/null; then
      return 1
    fi
    if [[ -s "$partial_req_file" ]]; then
      cat "$partial_req_file" >> "$req_file"
    fi
  done

  if [[ ! -s "$req_file" ]]; then
    echo "[ERROR] No CPU requirements found in helm.v3 deploymentProfiles.requiredResources.cpu"
    echo "        margo files: ${margo_yamls[*]}"
    return 1
  fi

  local duplicate_names
  duplicate_names="$(awk -F'\t' '{ count[$1]++ } END { for (n in count) if (count[n] > 1) print n }' "$req_file" | sort)"
  if [[ -n "$duplicate_names" ]]; then
    echo "[ERROR] Duplicate CPU component names found across provided app packages." >&2
    echo "        Each requiredResources.cpu[].name must be globally unique." >&2
    while IFS= read -r dup; do
      [[ -n "$dup" ]] && echo "          - $dup" >&2
    done <<< "$duplicate_names"
    return 1
  fi

  # Load topology from cache (builds it if absent)
  read_cpu_topology_cache "$cache_file"

  local -a sorted_ids=("${_TOPO_SORTED_IDS[@]}")
  local reserved_cpu=0

  if [[ "${#sorted_ids[@]}" -eq 0 ]]; then
    echo "[ERROR] No CPUs discovered from host topology cache."
    return 1
  fi

  declare -A available_map=()
  local cpu_id
  for cpu_id in "${sorted_ids[@]}"; do
    available_map["$cpu_id"]=1
  done

  if ! _nri_collect_existing_managed_cpus "$existing_used_file" "${NRI_BALLOON_RELEASE_NAME:-nri-resource-policy-balloons}"; then
    echo "[WARN] Failed to collect existing NRI balloon policies; continuing without subtraction."
  fi

  if [[ -s "$existing_used_file" ]]; then
    while IFS= read -r used_cpu; do
      [[ "$used_cpu" =~ ^[0-9]+$ ]] || continue
      unset 'available_map[$used_cpu]'
    done < "$existing_used_file"
  fi

  local -a available_ids=()
  for cpu_id in "${sorted_ids[@]}"; do
    [[ -n "${available_map[$cpu_id]:-}" ]] && available_ids+=("$cpu_id")
  done

  if [[ "${#available_ids[@]}" -eq 0 ]]; then
    echo "[ERROR] No CPUs available after subtracting existing NRI policies."
    return 1
  fi

  local reserved_cpuset="cpuset:${reserved_cpu}"

  declare -A reserved_map=()
  mark_cpu_set_from_range_list "${reserved_cpuset#cpuset:}" reserved_map

  local -a allocatable_ids=()
  for cpu_id in "${available_ids[@]}"; do
    [[ -n "${reserved_map[$cpu_id]:-}" ]] && continue
    allocatable_ids+=("$cpu_id")
  done

  if [[ "${#allocatable_ids[@]}" -eq 0 ]]; then
    echo "[ERROR] No allocatable CPUs after removing reservedResources (${reserved_cpuset})."
    return 1
  fi

  local available_cpuset
  available_cpuset="$(_nri_compact_cpuset "${available_ids[@]}")"

  declare -A consumed_map=()
  local -a balloon_blocks=()
  local req_name req_cores req_class req_type

  while IFS=$'\t' read -r req_name req_cores req_class req_type; do
    [[ -z "$req_name" || -z "$req_cores" || -z "$req_class" || -z "$req_type" ]] && continue

    if [[ ! "$req_cores" =~ ^[0-9]+$ ]] || (( req_cores <= 0 )); then
      echo "[ERROR] Invalid cores value for CPU requirement '$req_name': '$req_cores'"
      return 1
    fi

    if [[ "$req_type" != "isolated" && "$req_type" != "shared" ]]; then
      echo "[ERROR] CPU requirement '$req_name' has unsupported type '$req_type' (expected isolated/shared)."
      return 1
    fi

    local -a pool=()
    for cpu_id in "${allocatable_ids[@]}"; do
      [[ -n "${consumed_map[$cpu_id]:-}" ]] && continue
      local meta="${_TOPO_CORE_META[$cpu_id]}"
      local _arch="${meta%%|*}" rest="${meta#*|}"
      local cpu_class="${rest%%|*}" cpu_type="${rest##*|}"
      [[ "$cpu_class" == "$req_class" ]] || continue
      [[ "$cpu_type" == "$req_type" ]] || continue
      pool+=("$cpu_id")
    done

    if (( ${#pool[@]} < req_cores )); then
      echo "[ERROR] Not enough CPUs for requirement '$req_name' (class=$req_class type=$req_type cores=$req_cores)."
      echo "        Available matching CPUs: ${#pool[@]}"
      return 1
    fi

    local -a selected=()
    local i
    for ((i = 0; i < req_cores; i++)); do
      selected+=("${pool[$i]}")
      consumed_map["${pool[$i]}"]=1
    done

    local balloon_name="balloon_${req_name}"
    local prefer_isol="false"
    [[ "$req_type" == "isolated" ]] && prefer_isol="true"

    local block=""
    block+="    - name: ${balloon_name}"$'\n'
    block+="      allocatorPriority: high"$'\n'
    block+="      minBalloons: 1"$'\n'
    block+="      maxBalloons: 1"$'\n'
    block+="      minCPUs: ${req_cores}"$'\n'
    block+="      maxCPUs: ${req_cores}"$'\n'
    block+="      preferCoreType: ${req_class}"$'\n'
    block+="      preferIsolCpus: ${prefer_isol}"$'\n'
    block+="      preferCloseToDevices:"$'\n'

    for cpu_id in "${selected[@]}"; do
      block+="        - /sys/devices/system/cpu/cpu${cpu_id}/cache/index2"$'\n'
    done

    balloon_blocks+=("$block")
  done < "$req_file"

  if [[ "${#balloon_blocks[@]}" -eq 0 ]]; then
    echo "[ERROR] No valid CPU requirements found to build balloons."
    return 1
  fi

  # ---- emit YAML ---------------------------------------------------------
  {
    cat <<'HEADER'
# Balloon resource policy generated by device-agent.sh
# One balloon per requiredResources.cpu object from margo.yaml.
# Naming: balloon_<cpu.name>
#
# Install:
#   helm repo add nri-plugins https://containers.github.io/nri-plugins
#   helm repo update
#   helm install nri-resource-policy-balloons \
#     nri-plugins/nri-resource-policy-balloons \
#     --namespace kube-system -f balloon-policy.yaml
HEADER

    cat <<PREAMBLE
nri:
  runtime:
    patchConfig: false
  plugin:
    index: 10

config:
  pinCPU: true
  pinMemory: false

  availableResources:
    cpu: "cpuset:${available_cpuset}"

  reservedResources:
    cpu: "${reserved_cpuset}"

  balloonTypes:
PREAMBLE

    local balloon_block
    for balloon_block in "${balloon_blocks[@]}"; do
      printf '%s' "$balloon_block"
    done
  } > "$output_file"

  echo "✅ Balloon policy written to: $output_file"
  echo "[INFO] Source margo file(s): ${margo_yamls[*]}"
  echo "[INFO] Allocatable CPUs considered: ${#allocatable_ids[@]}"
  echo "[INFO] Balloons generated: ${#balloon_blocks[@]}"
}

# ---------------------------------------------------------------------------
# install_balloon_nri_plugin [values_file [margo_app_paths_csv]]
#   If values_file is omitted or the path does not yet exist the balloon
#   policy is generated automatically from margo-package/margo.yaml first.
# ---------------------------------------------------------------------------
install_balloon_nri_plugin() {
  local values_file="${1:-$HOME/sandbox/balloon-policy.yaml}"
  local margo_app_paths="${2:-}"

  if [[ ! -f "$values_file" ]]; then
    echo "[INFO] No policy file found at '$values_file' — generating from margo app package..."
    if ! generate_balloon_policy "$margo_app_paths" "$values_file"; then
      echo "[ERROR] Failed to generate balloon policy."
      echo "        Usage: install_balloon_nri_plugin [values_file [margo_app_paths_csv]]"
      return 1
    fi
    echo ""
  fi

  echo "Installing NRI Balloon Resource Policy plugin..."

  if helm repo list 2>/dev/null | awk '{print $1}' | grep -q "^nri-plugins$"; then
    echo "✅ nri-plugins helm repo already added, skipping."
  else
    echo "Adding nri-plugins helm repo..."
    helm repo add nri-plugins https://containers.github.io/nri-plugins
  fi

  helm repo update

  helm install nri-resource-policy-balloons nri-plugins/nri-resource-policy-balloons \
    --namespace kube-system \
    -f "$values_file"

  echo "✅ NRI Balloon Resource Policy plugin installed."
}

# ---------------------------------------------------------------------------
# update_balloon_nri_plugin <values_file>
# ---------------------------------------------------------------------------
update_balloon_nri_plugin() {
  local values_file="${1}"
  if [[ -z "$values_file" ]]; then
    echo "[ERROR] Values file is required."
    echo "Usage: update_balloon_nri_plugin <balloons-values-file>"
    return 1
  fi
  if [[ ! -f "$values_file" ]]; then
    echo "[ERROR] Values file not found: $values_file"
    return 1
  fi

  echo "Upgrading NRI Balloon Resource Policy plugin..."

  helm upgrade nri-resource-policy-balloons nri-plugins/nri-resource-policy-balloons \
    --namespace kube-system \
    -f "$values_file"

  echo "✅ NRI Balloon Resource Policy plugin upgraded."
}

# ---------------------------------------------------------------------------
# uninstall_balloon_nri_plugin
# ---------------------------------------------------------------------------
uninstall_balloon_nri_plugin() {
  echo "Uninstalling NRI Balloon Resource Policy plugin..."
  helm uninstall nri-resource-policy-balloons -n kube-system
  echo "✅ NRI Balloon Resource Policy plugin uninstalled."
}
