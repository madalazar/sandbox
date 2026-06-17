#!/bin/bash
# modules/nri.sh - NRI Balloon Resource Policy plugin management
#
# Functions:
#   generate_default_nri_policy - Generate default NRI policy with per-core balloons from host topology
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


# ---------------------------------------------------------------------------
# generate_default_nri_policy
#
# Generates a default NRI Balloon Resource Policy with:
#   - Fixed available/reserved CPUs
#   - Individual balloons for each isolated CPU core (ipc<class><core_id>)
#   - Shared cores delegated to NRI's default balloon (no custom balloon)
#
# Naming convention for isolated balloons:
#   i<class_abbrev>c<core_id>
#   - i: isolated type prefix
#   - <class_abbrev>: first letter of class (p=performance, e=efficiency, l=low-power)
#   - c: separator constant
#   - <core_id>: CPU index (0-N)
#   Examples: ipc0, ipc1, ipe5, ilc2
#
# Usage: generate_default_nri_policy [available_cpus [reserved_cpus [output_file [cache_file]]]]
#   available_cpus: cpuset range string (default: 0-119)
#   reserved_cpus:  cpuset range string (default: 0,100,105)
#   output_file:    destination for YAML (default: $HOME/sandbox/balloon-policy.yaml)
#   cache_file:     topology cache file (default: $CPU_TOPOLOGY_CACHE_FILE)
# ---------------------------------------------------------------------------
generate_default_nri_policy() {
  local available_cpus_range="${1:-0-119}"
  local reserved_cpus_range="${2:-0,100,105}"
  local output_file="${3:-$HOME/sandbox/balloon-policy.yaml}"
  local cache_file="${4:-$CPU_TOPOLOGY_CACHE_FILE}"

  mkdir -p "$(dirname "$output_file")"

  # Load topology from cache (builds it if absent)
  read_cpu_topology_cache "$cache_file"

  local -a sorted_ids=("${_TOPO_SORTED_IDS[@]}")
  if [[ "${#sorted_ids[@]}" -eq 0 ]]; then
    echo "[ERROR] No CPUs discovered from host topology cache." >&2
    return 1
  fi

  # Mark reserved CPUs
  declare -A reserved_map=()
  mark_cpu_set_from_range_list "$reserved_cpus_range" reserved_map

  # Collect isolated CPUs from topology
  declare -A isolated_by_class=()  # [class] -> array of CPU IDs
  local cpu_id meta arch class type

  for cpu_id in "${sorted_ids[@]}"; do
    meta="${_TOPO_CORE_META[$cpu_id]:-}"
    if [[ -z "$meta" ]]; then
      continue
    fi

    # Parse: arch|class|type
    arch="${meta%%|*}"
    rest="${meta#*|}"
    class="${rest%%|*}"
    type="${rest##*|}"

    # Only create balloons for isolated CPUs
    if [[ "$type" == "isolated" ]]; then
      isolated_by_class["$cpu_id"]="$class"
    fi
  done

  if [[ "${#isolated_by_class[@]}" -eq 0 ]]; then
    echo "[WARN] No isolated CPUs found in topology. Generating policy with only shared cores." >&2
  fi

  # Generate balloon blocks for each isolated CPU
  local -a balloon_blocks=()
  local class_abbrev

  for cpu_id in "${sorted_ids[@]}"; do
    [[ -z "${isolated_by_class[$cpu_id]:-}" ]] && continue

    class="${isolated_by_class[$cpu_id]}"
    case "$class" in
      performance) class_abbrev="p" ;;
      efficiency) class_abbrev="e" ;;
      low-power)  class_abbrev="l" ;;
      *)          class_abbrev="?" ;;
    esac

    local balloon_name="i${class_abbrev}c${cpu_id}"
    local block=""
    block+="    - name: ${balloon_name}"$'\n'
    block+="      allocatorPriority: high"$'\n'
    block+="      minBalloons: 1"$'\n'
    block+="      maxBalloons: 1"$'\n'
    block+="      minCPUs: 1"$'\n'
    block+="      maxCPUs: 1"$'\n'
    block+="      preferIsolCpus: true"$'\n'
    block+="      preferCloseToDevices:"$'\n'
    block+="        - /sys/devices/system/cpu/cpu${cpu_id}/cache/index2"$'\n'

    balloon_blocks+=("$block")
  done

  # Build compact cpuset strings
  local available_cpuset="cpuset:${available_cpus_range}"
  local reserved_cpuset="cpuset:${reserved_cpus_range}"

  # Emit YAML
  {
    cat <<'HEADER'
# Default NRI Balloon Resource Policy
# Generated with per-core isolated balloons from host topology
#
# This policy defines:
#   - availableResources: fixed CPU range (configured at generation time)
#   - reservedResources: fixed reserved CPU range
#   - Per-core balloons for each isolated CPU (ipc<class><core_id>)
#   - Shared cores: delegated to NRI's default balloon (no custom definition needed)
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
    cpu: "${available_cpuset}"

  reservedResources:
    cpu: "${reserved_cpuset}"

  balloonTypes:
PREAMBLE

    if [[ "${#balloon_blocks[@]}" -gt 0 ]]; then
      local balloon_block
      for balloon_block in "${balloon_blocks[@]}"; do
        printf '%s' "$balloon_block"
      done
    else
      cat <<'NO_BALLOONS'
    # No isolated CPUs in topology; only shared cores available
    # Shared cores will use NRI's default balloon
NO_BALLOONS
    fi
  } > "$output_file"

  local isolated_count="${#balloon_blocks[@]}"
  echo "✅ Default balloon policy written to: $output_file"
  echo "[INFO] Available CPUs: ${available_cpuset}"
  echo "[INFO] Reserved CPUs: ${reserved_cpuset}"
  echo "[INFO] Isolated balloons generated: ${isolated_count}"
  echo "[INFO] Shared cores will use NRI default balloon"
}


# ---------------------------------------------------------------------------
# install_balloon_nri_plugin [values_file]
#   If values_file is omitted or the path does not yet exist the default
#   balloon policy is generated automatically from host CPU topology.
# ---------------------------------------------------------------------------
install_balloon_nri_plugin() {
  local values_file="${1:-$HOME/sandbox/balloon-policy.yaml}"

  if [[ ! -f "$values_file" ]]; then
    echo "[INFO] No policy file found at '$values_file' — generating default policy from host topology..."
    if ! generate_default_nri_policy "" "" "$values_file"; then
      echo "[ERROR] Failed to generate default NRI balloon policy."
      echo "        Usage: install_balloon_nri_plugin [values_file]"
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
