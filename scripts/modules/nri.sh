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
source "${SCRIPT_DIR_NRI}/cache-topology.sh"

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

_nri_expand_range_count() {
  local range_list="$1"
  declare -A tmp=()
  mark_cpu_set_from_range_list "$range_list" tmp
  echo "${#tmp[@]}"
}

_nri_extract_available_cpus_from_policy() {
  local policy_file="$1"
  [[ -r "$policy_file" ]] || return 1

  local cpu_line
  cpu_line="$(awk '
    /^[[:space:]]*availableResources:[[:space:]]*$/ {in_avail=1; next}
    in_avail && /^[[:space:]]*reservedResources:[[:space:]]*$/ {in_avail=0}
    in_avail && /^[[:space:]]*cpu:[[:space:]]*/ {
      print
      exit
    }
  ' "$policy_file" 2>/dev/null || true)"

  [[ -n "$cpu_line" ]] || return 1

  local value
  value="$(sed -E 's/^[[:space:]]*cpu:[[:space:]]*"?cpuset:([^"[:space:]]+)"?.*$/\1/' <<< "$cpu_line")"
  [[ -n "$value" && "$value" != "$cpu_line" ]] || return 1

  echo "$value"
}

_nri_load_l3_membership_from_sysfs() {
  declare -gA _NRI_L3_TOTAL_CPUS=() _NRI_L3_MEMBER=()
  declare -A _nri_l3_shared_to_id=()
  local _nri_next_l3_id=0

  local cpu_path index_path
  for cpu_path in /sys/devices/system/cpu/cpu[0-9]*; do
    [[ -d "$cpu_path" ]] || continue
    for index_path in "$cpu_path"/cache/index*; do
      [[ -d "$index_path" ]] || continue

      local level_num shared_list l3_id
      level_num="$(cat "$index_path/level" 2>/dev/null || true)"
      [[ "$level_num" == "3" ]] || continue

      shared_list="$(tr -d '[:space:]' < "$index_path/shared_cpu_list" 2>/dev/null || true)"
      [[ -n "$shared_list" ]] || continue

      l3_id=""
      if [[ -r "$index_path/id" ]]; then
        l3_id="$(tr -d '[:space:]' < "$index_path/id" 2>/dev/null || true)"
      fi
      if [[ ! "$l3_id" =~ ^[0-9]+$ ]]; then
        if [[ -n "${_nri_l3_shared_to_id[$shared_list]:-}" ]]; then
          l3_id="${_nri_l3_shared_to_id[$shared_list]}"
        else
          l3_id="$_nri_next_l3_id"
          _nri_l3_shared_to_id["$shared_list"]="$l3_id"
          _nri_next_l3_id=$((_nri_next_l3_id + 1))
        fi
      fi

      local member
      declare -A local_set=()
      mark_cpu_set_from_range_list "$shared_list" local_set
      _NRI_L3_TOTAL_CPUS["$l3_id"]="${#local_set[@]}"

      for member in "${!local_set[@]}"; do
        _NRI_L3_MEMBER["${l3_id}|${member}"]=1
      done
    done
  done

  [[ ${#_NRI_L3_TOTAL_CPUS[@]} -gt 0 ]]
}

_nri_load_l3_ways_from_cache_file() {
  local cache_file="$1"
  declare -gA _NRI_L3_WAYS=()

  [[ -r "$cache_file" ]] || return 1

  local level cache_id allocation_types size ways way_size
  while IFS=$'\t' read -r level cache_id allocation_types size ways way_size; do
    [[ -n "$level" ]] || continue
    [[ "$level" =~ ^# ]] && continue
    [[ "$level" == "L3" ]] || continue
    [[ "$cache_id" =~ ^L#([0-9]+)$ ]] || continue
    local l3_id="${BASH_REMATCH[1]}"
    [[ "$ways" =~ ^[0-9]+$ ]] || continue
    (( ways > 0 )) || continue
    _NRI_L3_WAYS["$l3_id"]="$ways"
  done < "$cache_file"

  [[ ${#_NRI_L3_WAYS[@]} -gt 0 ]]
}

_nri_range_to_bitmask() {
  local start="$1"
  local end="$2"

  if (( start > end )); then
    return 1
  fi

  local mask=0
  local i
  for (( i = start; i <= end; i++ )); do
    mask=$(( mask | (1 << i) ))
  done

  printf '0x%x' "$mask"
}

_nri_format_way_range() {
  local start="$1"
  local end="$2"

  _nri_range_to_bitmask "$start" "$end"
}

_nri_build_rdt_control_yaml() {
  local available_range="$1"
  local cache_file="$2"

  # This function now generates a minimal RDT configuration with fullCache: null
  # to override default chart values and allow dynamic partition creation.

  local rdt_yaml=""
  rdt_yaml+="  control:"$'\n'
  rdt_yaml+="    rdt:"$'\n'
  rdt_yaml+="      enable: true"$'\n'
  rdt_yaml+="      usePodQoSAsDefaultClass: false"$'\n'
  rdt_yaml+="      options:"$'\n'
  rdt_yaml+="        l3:"$'\n'
  rdt_yaml+="          optional: true"$'\n'
  rdt_yaml+="      partitions:"$'\n'
  rdt_yaml+="        fullCache: null"$'\n'

  printf '%s' "$rdt_yaml"
  return 0
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
#   available_cpus: optional cpuset range override (default: all discovered physical cores)
#   reserved_cpus:  optional cpuset range override (default: core 0 + last 2 non-isolated physical cores)
#   output_file:    destination for YAML (default: $HOME/sandbox/balloon-policy.yaml)
#   cache_file:     topology cache file (default: $CPU_TOPOLOGY_CACHE_FILE)
# ---------------------------------------------------------------------------
generate_default_nri_policy() {
  local available_cpus_range="${1:-}"
  local reserved_cpus_range="${2:-}"
  local output_file="${3:-$HOME/sandbox/balloon-policy.yaml}"
  local cache_file="${4:-$CPU_TOPOLOGY_CACHE_FILE}"

  local cache_topology_file="${CACHE_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cache-topology.tsv}"

  mkdir -p "$(dirname "$output_file")"

  # Load topology from cache (builds it if absent)
  read_cpu_topology_cache "$cache_file"

  local -a sorted_ids=("${_TOPO_SORTED_IDS[@]}")
  if [[ "${#sorted_ids[@]}" -eq 0 ]]; then
    echo "[ERROR] No CPUs discovered from host topology cache." >&2
    return 1
  fi

  # Default availableResources to all discovered physical cores.
  if [[ -z "$available_cpus_range" ]]; then
    available_cpus_range="$(_nri_compact_cpuset "${sorted_ids[@]}")"
  fi

  # Mark CPUs included in availableResources.
  declare -A available_map=()
  mark_cpu_set_from_range_list "$available_cpus_range" available_map

  # Build a deterministic, sorted list of available physical cores.
  local -a available_ids=()
  for cpu_id in "${sorted_ids[@]}"; do
    [[ -n "${available_map[$cpu_id]:-}" ]] && available_ids+=("$cpu_id")
  done
  if [[ "${#available_ids[@]}" -eq 0 ]]; then
    echo "[ERROR] availableResources resolves to no physical cores: '$available_cpus_range'" >&2
    return 1
  fi

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

  # Default reservedResources to:
  #   - core 0 (if it is part of availableResources)
  #   - plus the highest two non-isolated cores in availableResources (excluding core 0)
  if [[ -z "$reserved_cpus_range" ]]; then
    declare -A reserved_selected=()

    if [[ -n "${available_map[0]:-}" ]]; then
      reserved_selected[0]=1
    fi

    local -a non_isolated_candidates=()
    for cpu_id in "${available_ids[@]}"; do
      meta="${_TOPO_CORE_META[$cpu_id]:-}"
      [[ -z "$meta" ]] && continue
      arch="${meta%%|*}"
      rest="${meta#*|}"
      class="${rest%%|*}"
      type="${rest##*|}"

      if [[ "$type" != "isolated" && "$cpu_id" != "0" ]]; then
        non_isolated_candidates+=("$cpu_id")
      fi
    done

    local candidate_count="${#non_isolated_candidates[@]}"
    local take_start=0
    if (( candidate_count > 2 )); then
      take_start=$((candidate_count - 2))
    fi

    local i
    for (( i = take_start; i < candidate_count; i++ )); do
      reserved_selected["${non_isolated_candidates[$i]}"]=1
    done

    local -a reserved_ids=()
    for cpu_id in "${available_ids[@]}"; do
      [[ -n "${reserved_selected[$cpu_id]:-}" ]] && reserved_ids+=("$cpu_id")
    done

    if [[ "${#reserved_ids[@]}" -gt 0 ]]; then
      reserved_cpus_range="$(_nri_compact_cpuset "${reserved_ids[@]}")"
    fi
  fi

  # Keep reservedResources constrained to availableResources.
  if [[ -n "$reserved_cpus_range" ]]; then
    declare -A requested_reserved_map=()
    mark_cpu_set_from_range_list "$reserved_cpus_range" requested_reserved_map

    local -a constrained_reserved_ids=()
    for cpu_id in "${available_ids[@]}"; do
      [[ -n "${requested_reserved_map[$cpu_id]:-}" ]] && constrained_reserved_ids+=("$cpu_id")
    done

    if [[ "${#constrained_reserved_ids[@]}" -gt 0 ]]; then
      reserved_cpus_range="$(_nri_compact_cpuset "${constrained_reserved_ids[@]}")"
    else
      reserved_cpus_range=""
    fi
  fi

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

  # Build RDT control block only if cache topology data is available and usable.
  local rdt_control_yaml=""
  if [[ -s "$cache_topology_file" ]]; then
    rdt_control_yaml="$(_nri_build_rdt_control_yaml "$available_cpus_range" "$cache_topology_file" 2>/dev/null || true)"
  fi

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

    if [[ -n "$rdt_control_yaml" ]]; then
      printf '%s\n' "$rdt_control_yaml"
    fi
  } > "$output_file"

  local isolated_count="${#balloon_blocks[@]}"
  echo "✅ Default balloon policy written to: $output_file"
  echo "[INFO] Available CPUs: ${available_cpuset}"
  echo "[INFO] Reserved CPUs: ${reserved_cpuset}"
  echo "[INFO] Isolated balloons generated: ${isolated_count}"
  echo "[INFO] Shared cores will use NRI default balloon"
  if [[ -n "$rdt_control_yaml" ]]; then
    echo "[INFO] RDT control/partitions generated from cache topology: $cache_topology_file"
  else
    echo "[INFO] RDT control/partitions skipped (no usable cache topology at $cache_topology_file)"
  fi
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
