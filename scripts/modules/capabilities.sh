#!/bin/bash
# modules/capabilities.sh - capabilities.json updater from persisted host topology
#
# README
# This script updates capabilities.json properties.cpus.
# CPU data is grouped by host topology.
#
# properties.cpus contains an array of CPU objects grouped by core type + class:
# 1) cores
#    - Number of physical cores discovered for this core type and class after SMT sibling collapse.
#
# 2) class (performance/efficiency/low-power)
#    - Uses cpuinfo_max_freq per selected physical CPU.
#    - Unique max frequencies are sorted high-to-low.
#    - Highest tier => performance.
#    - Lowest tier => low-power only when 3+ distinct tiers exist, otherwise efficiency.
#    - Any middle tier => efficiency.
#
# 3) type
#    - Derived from /sys/devices/system/cpu/isolated.
#    - CPUs listed there are marked type=isolated; all others are type=shared.
#
# 4) architecture
#    - Derived from uname -m and mapped to API values:
#      x86_64/amd64 -> amd64, aarch64/arm64 -> arm64, arm* -> arm.
#
# Physical cores only:
# - SMT/Hyperthread siblings are collapsed by reading
#   /sys/devices/system/cpu/cpu*/topology/thread_siblings_list.
# - One representative CPU per sibling group is counted.

SCRIPT_DIR_CAP="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091  # Runtime path is relative to this module.
source "${SCRIPT_DIR_CAP}/cpu-topology.sh"

CAPABILITIES_FILE="${CAPABILITIES_FILE:-$HOME/sandbox/poc/device/agent/config/capabilities.json}"

_validate_capabilities_update_inputs() {
  local capabilities_file="$1"

  if [[ ! -f "$capabilities_file" ]]; then
    echo "ERROR: capabilities.json not found at $capabilities_file" >&2
    return 1
  fi

  if ! command -v yq >/dev/null 2>&1; then
    echo "ERROR: yq is required to update capabilities.json" >&2
    return 1
  fi
}

_persist_capabilities_update() {
  local capabilities_file="$1"
  local tmp_file="$2"

  if ! chmod --reference="$capabilities_file" "$tmp_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to preserve permissions for $capabilities_file" >&2
    return 1
  fi

  if ! mv "$tmp_file" "$capabilities_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to persist updated capabilities file" >&2
    return 1
  fi
}

# Validate CPU topology JSON and count physical cores by scheduling type and class.
# Populates the associative array named by $2 with "type|class" counts and sets
# the integer variable named by $3 to the total core count. Returns 1 for an
# invalid class/type or an empty topology; otherwise returns 0 and prints nothing.
_count_and_group_cores() {
  local topology_json="$1"
  local -n core_counts_ref="$2"
  local -n total_cores_ref="$3"

  local cpu_id cpu_class cpu_type
  while IFS=$'\t' read -r cpu_id cpu_class cpu_type; do
    case "$cpu_class" in
      performance|efficiency|low-power) ;;
      *)
        echo "ERROR: Invalid CPU class '$cpu_class' for CPU $cpu_id" >&2
        return 1
        ;;
    esac

    case "$cpu_type" in
      isolated|shared) ;;
      *)
        echo "ERROR: Invalid CPU type '$cpu_type' for CPU $cpu_id" >&2
        return 1
        ;;
    esac

    local kind_key="${cpu_type}|${cpu_class}"
    core_counts_ref["$kind_key"]=$(( ${core_counts_ref["$kind_key"]:-0} + 1 ))
    total_cores_ref=$((total_cores_ref + 1))
  done < <(yq eval -r '.[] | [.id, .class, .type] | @tsv' <<< "$topology_json")

  if [[ "$total_cores_ref" -le 0 ]]; then
    echo "ERROR: CPU topology contains no physical cores" >&2
    return 1
  fi
}

# Convert a "type|class" core-count associative array and architecture into a deterministically
# ordered JSON array of CPU capability objects containing cores, class, type, and architecture.
_build_cpus_json() {
  local -n kind_counts_ref="$1"
  local cpu_arch="$2"
  local -a sorted_kind_keys=()
  local kind_key
  while IFS= read -r kind_key; do
    [[ -n "$kind_key" ]] && sorted_kind_keys+=("$kind_key")
  done < <(printf '%s\n' "${!kind_counts_ref[@]}" | sort)

  # Single-pass assembly: collect JSON objects in a Bash array and format with yq in one pass.
  # NOTE: Direct string interpolation into JSON without escaping is vulnerable to
  # escaping/syntax issues if variable values ever contain characters like '"', '$', or '\'.
  local -a items=()
  local cpu_type cpu_class
  for kind_key in "${sorted_kind_keys[@]}"; do
    IFS='|' read -r cpu_type cpu_class <<< "$kind_key"
    local cores="${kind_counts_ref[$kind_key]}"
    items+=("{\"cores\":$cores,\"class\":\"$cpu_class\",\"type\":\"$cpu_type\",\"architecture\":\"$cpu_arch\"}")
  done

  local json_raw
  json_raw="$(IFS=,; echo "[${items[*]}]")"

  if ! yq eval -o=json -I=0 '.' - <<< "$json_raw"; then
    echo "ERROR: Failed to construct CPU capabilities JSON" >&2
    return 1
  fi
}

update_cpu_capabilities() {
  local capabilities_file="${1:-$CAPABILITIES_FILE}"
  local topology_file="${2:-$CPU_TOPOLOGY_CACHE_FILE}"

  _validate_capabilities_update_inputs "$capabilities_file" || return 1

  local topology_json
  if ! topology_json="$(read_cpu_topology_as_json "$topology_file")"; then
    echo "ERROR: Failed to load CPU topology from $topology_file" >&2
    return 1
  fi

  # Count physical cores by scheduling type and CPU class.
  # shellcheck disable=SC2034  # core_count_by_kind and total_cores are populated/consumed via namerefs.
  declare -A core_count_by_kind=()
  # shellcheck disable=SC2034
  local total_cores=0
  _count_and_group_cores "$topology_json" core_count_by_kind total_cores || return 1

  local cpu_arch
  if ! cpu_arch="$(map_machine_arch_to_capability_arch "$(uname -m)")"; then
    return 1
  fi

  local cpus_json
  cpus_json="$(_build_cpus_json core_count_by_kind "$cpu_arch")" || return 1

  local tmp_file
  if ! tmp_file="$(mktemp "${capabilities_file}.tmp.XXXXXX")"; then
    echo "ERROR: Failed to create a temporary file beside $capabilities_file" >&2
    return 1
  fi

  if ! yq eval -e '((.properties | type) == "!!map") and ((.properties.cpus | type) == "!!seq")' - < "$capabilities_file" >/dev/null 2>&1; then
    rm -f "$tmp_file"
    echo "ERROR: Refusing CPU update: properties.cpus must be an array in $capabilities_file" >&2
    return 1
  fi

  if ! CPUS_JSON="$cpus_json" yq eval -o=json -P '.properties.cpus = env(CPUS_JSON)' - < "$capabilities_file" > "$tmp_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to update properties.cpus in $capabilities_file" >&2
    return 1
  fi

  if ! chmod --reference="$capabilities_file" "$tmp_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to preserve permissions for $capabilities_file" >&2
    return 1
  fi

  if ! mv "$tmp_file" "$capabilities_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to persist updated capabilities file" >&2
    return 1
  fi

  echo "Updated CPU capabilities in $capabilities_file"
  return 0
}

update_capabilities_resources_from_host() {
  local capabilities_file="${1:-$CAPABILITIES_FILE}"
  local cpu_topology_file="${2:-$CPU_TOPOLOGY_CACHE_FILE}"

  update_cpu_capabilities "$capabilities_file" "$cpu_topology_file" || return 1

  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  update_capabilities_resources_from_host "$@"
fi
