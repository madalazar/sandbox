#!/bin/bash
# modules/capabilities.sh - Host capability discovery and capabilities.json updater
#
# README
# This script updates capabilities.json resources.cpu by grouping host CPUs using:
# 1) architecture
#    - Derived from uname -m and mapped to API values:
#      x86_64/amd64 -> amd64, aarch64/arm64 -> arm64, arm* -> arm.
#
# 2) core type (isolated/shared)
#    - Derived from /sys/devices/system/cpu/isolated.
#    - CPUs listed there are marked type=isolated; all others are type=shared.
#
# 3) cpu class (performance/efficiency/low-power)
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
source "${SCRIPT_DIR_CAP}/cpu-topology.sh"

update_capabilities_cpu_from_host() {
  local capabilities_file="${1:-$HOME/sandbox/poc/device/agent/config/capabilities.json}"
  local cache_file="${2:-$CPU_TOPOLOGY_CACHE_FILE}"

  if [[ ! -f "$capabilities_file" ]]; then
    echo "ERROR: capabilities.json not found at $capabilities_file"
    return 1
  fi

  # Load topology from cache (builds it if absent)
  read_cpu_topology_cache "$cache_file"

  # ---- group cores into buckets: type|class|arch -> count ----------------
  declare -A cpu_buckets=()
  local cpu_id
  for cpu_id in "${_TOPO_SORTED_IDS[@]}"; do
    local meta="${_TOPO_CORE_META[$cpu_id]}"
    local b_arch="${meta%%|*}" rest="${meta#*|}"
    local b_class="${rest%%|*}" b_type="${rest##*|}"
    local bucket_key="${b_type}|${b_class}|${b_arch}"
    cpu_buckets["$bucket_key"]=$(( ${cpu_buckets["$bucket_key"]:-0} + 1 ))
  done

  local cpu_json=""
  local first=true
  local stable_keys=()
  while IFS= read -r key; do
    [[ -n "$key" ]] && stable_keys+=("$key")
  done < <(printf '%s\n' "${!cpu_buckets[@]}" | sort)

  local key
  for key in "${stable_keys[@]}"; do
    local cpu_type="${key%%|*}"
    local rest="${key#*|}"
    local cpu_class="${rest%%|*}"
    local cpu_arch="${rest##*|}"
    local cpu_cores="${cpu_buckets[$key]}"

    [[ "$cpu_cores" -le 0 ]] && continue

    if [[ "$first" == true ]]; then
      first=false
    else
      cpu_json+=$',\n'
    fi

    cpu_json+="{\"architecture\":\"${cpu_arch}\",\"cores\":${cpu_cores},\"class\":\"${cpu_class}\",\"type\":\"${cpu_type}\"}"
  done

  if [[ -z "$cpu_json" ]]; then
    echo "ERROR: Failed to construct CPU capabilities JSON"
    return 1
  fi

  local cpu_array_json="[$cpu_json]"

  if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required to update capabilities.json"
    return 1
  fi

  local tmp_file
  tmp_file="$(mktemp)"

  if ! jq --argjson cpu "$cpu_array_json" '
    def cpu_item_ok:
      (type == "object")
      and (.architecture | type == "string")
      and (.cores | type == "number")
      and (.class | type == "string")
      and (.type | type == "string");

    def cpu_shape_similar_at($path):
      (getpath($path) | type) == "object"
      and (
        ((getpath($path + ["cpu"]) | type) == "array" and (getpath($path + ["cpu"]) | all(.[]; cpu_item_ok)))
        or
        ((getpath($path + ["cpu"]) | type) == "object" and (getpath($path + ["cpu"]) | cpu_item_ok))
      );

    if cpu_shape_similar_at(["properties", "resources"]) then
      setpath(["properties", "resources", "cpu"]; $cpu)
    elif cpu_shape_similar_at(["resources"]) then
      setpath(["resources", "cpu"]; $cpu)
    else
      error("Refusing CPU update: existing properties.resources.cpu/resources.cpu schema is not similar to expected CPU capability entries")
    end
  ' "$capabilities_file" > "$tmp_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to update resources.cpu in $capabilities_file"
    return 1
  fi

  if ! mv "$tmp_file" "$capabilities_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to persist updated capabilities file"
    return 1
  fi

  echo "Updated CPU capabilities in $capabilities_file"
  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  update_capabilities_cpu_from_host "$@"
fi
