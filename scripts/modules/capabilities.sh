#!/bin/bash
# modules/capabilities.sh - capabilities.json updater from persisted host topology
#
# README
# This script updates capabilities.json properties.cpus.
# CPU data is grouped by host topology.
#
# properties.cpus contains one host CPU object with:
# 1) top-level architecture
#    - Derived from uname -m and mapped to API values:
#      x86_64/amd64 -> amd64, aarch64/arm64 -> arm64, arm* -> arm.
#
# 2) top-level cores
#    - Total physical cores discovered after SMT sibling collapse.
#
# 3) kinds[] grouped by core type + class
#    - Derived from /sys/devices/system/cpu/isolated.
#    - CPUs listed there are marked type=isolated; all others are type=shared.
#
# 4) cpu class (performance/efficiency/low-power)
#    - Uses cpuinfo_max_freq per selected physical CPU.
#    - Unique max frequencies are sorted high-to-low.
#    - Highest tier => performance.
#    - Lowest tier => low-power only when 3+ distinct tiers exist, otherwise efficiency.
#    - Any middle tier => efficiency.
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

  if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required to update capabilities.json" >&2
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
  done < <(jq -r '.[] | [.id, .class, .type] | @tsv' <<< "$topology_json")

  if [[ "$total_cores_ref" -le 0 ]]; then
    echo "ERROR: CPU topology contains no physical cores" >&2
    return 1
  fi
}

# Convert a "type|class" core-count associative array into a deterministically
# ordered JSON array of objects containing cores, class, and type.
_build_cpu_kinds_json() {
  local -n kind_counts_ref="$1"
  local -a sorted_kind_keys=()
  local kind_key
  while IFS= read -r kind_key; do
    [[ -n "$kind_key" ]] && sorted_kind_keys+=("$kind_key")
  done < <(printf '%s\n' "${!kind_counts_ref[@]}" | sort)

  local kinds_json='[]'
  local cpu_type cpu_class
  for kind_key in "${sorted_kind_keys[@]}"; do
    IFS='|' read -r cpu_type cpu_class <<< "$kind_key"

    if ! kinds_json="$(jq -c \
      --argjson cores "${kind_counts_ref[$kind_key]}" \
      --arg class "$cpu_class" \
      --arg type "$cpu_type" \
      '. + [{cores: $cores, class: $class, type: $type}]' \
      <<< "$kinds_json")"; then
      echo "ERROR: Failed to construct CPU kind JSON" >&2
      return 1
    fi
  done

  printf '%s\n' "$kinds_json"
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
  # shellcheck disable=SC2034  # Arrays are consumed through namerefs.
  declare -A core_count_by_kind=()
  local total_cores=0
  _count_and_group_cores "$topology_json" core_count_by_kind total_cores || return 1

  local cpu_arch
  if ! cpu_arch="$(map_machine_arch_to_capability_arch "$(uname -m)")"; then
    return 1
  fi

  local cpu_kinds_json
  cpu_kinds_json="$(_build_cpu_kinds_json core_count_by_kind)" || return 1

  local cpu_object_json
  if ! cpu_object_json="$(
    jq -cn \
      --argjson cores "$total_cores" \
      --arg architecture "$cpu_arch" \
      --argjson kinds "$cpu_kinds_json" \
      '{
        cores: $cores,
        architecture: $architecture,
        kinds: $kinds
      }'
  )"; then
    echo "ERROR: Failed to construct CPU capabilities JSON" >&2
    return 1
  fi

  local tmp_file
  if ! tmp_file="$(mktemp "${capabilities_file}.tmp.XXXXXX")"; then
    echo "ERROR: Failed to create a temporary file beside $capabilities_file" >&2
    return 1
  fi

  if ! jq --argjson cpu "$cpu_object_json" '
    if (.properties | type) == "object" and (.properties.cpus | type) == "array" then
      .properties.cpus = [$cpu]
    else
      error("Refusing CPU update: properties.cpus must be an array")
    end
  ' "$capabilities_file" > "$tmp_file"; then
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
