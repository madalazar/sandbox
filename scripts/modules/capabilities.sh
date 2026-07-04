#!/bin/bash
# modules/capabilities.sh - Host capability discovery and capabilities.json updater
#
# README
# This script updates capabilities.json resources.cpu and resources.cache.
# CPU data is grouped by host topology; cache data is discovered from lstopo
# (with sysfs fallback) and augmented by resctrl CAT allocation support.
#
# If Intel RDT appears supported in CPU flags but is not enabled/mounted yet,
# cache update prints the commands needed to enable it and exits early.
#
# resources.cpu uses:
# 1) top-level architecture
#    - Derived from uname -m and mapped to API values:
#      x86_64/amd64 -> amd64, aarch64/arm64 -> arm64, arm* -> arm.
#
# 2) top-level cores
#    - Total physical cores discovered after SMT sibling collapse.
#
# 3) cpu.kinds[] grouped by core type + class
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
source "${SCRIPT_DIR_CAP}/cpu-topology.sh"
source "${SCRIPT_DIR_CAP}/cache-topology.sh"

update_capabilities_cpu_from_host() {
  local capabilities_file="${1:-$HOME/sandbox/poc/device/agent/config/capabilities.json}"
  local cache_file="${2:-$CPU_TOPOLOGY_CACHE_FILE}"

  if [[ ! -f "$capabilities_file" ]]; then
    echo "ERROR: capabilities.json not found at $capabilities_file"
    return 1
  fi

  # Load topology from cache (builds it if absent)
  read_cpu_topology_cache "$cache_file"

  # ---- group cores into buckets: type|class -> count ---------------------
  declare -A cpu_buckets=()
  local total_cpu_cores=0
  local cpu_id
  for cpu_id in "${_TOPO_SORTED_IDS[@]}"; do
    local meta="${_TOPO_CORE_META[$cpu_id]}"
    local _b_arch="${meta%%|*}" rest="${meta#*|}"
    local b_class="${rest%%|*}" b_type="${rest##*|}"
    local bucket_key="${b_type}|${b_class}"
    cpu_buckets["$bucket_key"]=$(( ${cpu_buckets["$bucket_key"]:-0} + 1 ))
    total_cpu_cores=$((total_cpu_cores + 1))
  done

  local cpu_arch
  cpu_arch="$(map_machine_arch_to_capability_arch "$(uname -m)")"

  local cpu_kinds_json=""
  local first=true
  local stable_keys=()
  while IFS= read -r key; do
    [[ -n "$key" ]] && stable_keys+=("$key")
  done < <(printf '%s\n' "${!cpu_buckets[@]}" | sort)

  local key
  for key in "${stable_keys[@]}"; do
    local cpu_type="${key%%|*}"
    local cpu_class="${key#*|}"
    local cpu_cores="${cpu_buckets[$key]}"

    [[ "$cpu_cores" -le 0 ]] && continue

    if [[ "$first" == true ]]; then
      first=false
    else
      cpu_kinds_json+=','
    fi

    cpu_kinds_json+="{\"cores\":${cpu_cores},\"class\":\"${cpu_class}\",\"type\":\"${cpu_type}\"}"
  done

  if [[ -z "$cpu_kinds_json" || "$total_cpu_cores" -le 0 ]]; then
    echo "ERROR: Failed to construct CPU capabilities JSON"
    return 1
  fi

  local cpu_object_json
  cpu_object_json="{\"cores\":${total_cpu_cores},\"architecture\":\"${cpu_arch}\",\"kinds\":[${cpu_kinds_json}]}"

  if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required to update capabilities.json"
    return 1
  fi

  local tmp_file
  tmp_file="$(mktemp)"

  if ! jq --argjson cpu "$cpu_object_json" '
    def cpu_kind_ok:
      # New schema (cpu.kinds[] item): cores + class + type
      (type == "object")
      and (.cores | type == "number")
      and (.class | type == "string")
      and (.type | type == "string");

    def cpu_object_ok:
      # New schema (cpu object): cores + architecture + kinds[]
      (type == "object")
      and (.cores | type == "number")
      and ((has("architecture") | not) or (.architecture | type == "string"))
      and (
        (has("kinds") | not)
        or ((.kinds | type) == "array" and (.kinds | all(.[]; cpu_kind_ok)))
      );

    def cpu_legacy_item_ok:
      # Legacy schema item: architecture + cores (+ optional class/type)
      (type == "object")
      and (.cores | type == "number")
      and ((has("architecture") | not) or (.architecture | type == "string"))
      and ((has("class") | not) or (.class | type == "string"))
      and ((has("type") | not) or (.type | type == "string"));

    def cpu_item_compatible_ok:
      cpu_object_ok or cpu_legacy_item_ok;

    def cpu_shape_similar_at($path):
      (getpath($path) | type) == "object"
      and (
        ((getpath($path + ["cpu"]) | type) == "array" and (getpath($path + ["cpu"]) | all(.[]; cpu_legacy_item_ok)))
        or
        ((getpath($path + ["cpu"]) | type) == "object" and (getpath($path + ["cpu"]) | cpu_item_compatible_ok))
      );

    if cpu_shape_similar_at(["properties", "resources"]) then
      setpath(["properties", "resources", "cpu"]; $cpu)
    elif cpu_shape_similar_at(["resources"]) then
      setpath(["resources", "cpu"]; $cpu)
    else
      error("Refusing CPU update: existing properties.resources.cpu/resources.cpu schema is not similar to expected cpu object (cores, architecture, kinds[])")
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

update_capabilities_cache_from_host() {
  local capabilities_file="${1:-$HOME/sandbox/poc/device/agent/config/capabilities.json}"
  local cache_file="${2:-$CACHE_TOPOLOGY_CACHE_FILE}"

  if [[ ! -f "$capabilities_file" ]]; then
    echo "ERROR: capabilities.json not found at $capabilities_file"
    return 1
  fi

  if is_intel_rdt_capable_host; then
    if ! is_intel_rdt_enabled_and_usable; then
      ensure_resctrl_mounted || {
        print_intel_rdt_enable_instructions
        echo "[INFO] Exiting without cache capability update until Intel RDT is enabled."
        return 2
      }
    fi

    if ! is_intel_rdt_enabled_and_usable; then
      print_intel_rdt_enable_instructions
      echo "[INFO] Exiting without cache capability update until Intel RDT is enabled."
      return 2
    fi
  fi

  if [[ ! -f "$cache_file" ]]; then
    build_cache_topology_cache "$cache_file" || return 1
  fi

  local cache_array_json='[]'
  local level cache_id cache_id_num allocation_types size ways way_size_kb
  while IFS=$'\t' read -r level cache_id allocation_types size ways way_size_kb; do
    [[ "$level" == "L3" ]] || continue
    [[ "$cache_id" =~ ^L#[0-9]+$ ]] || continue
    cache_id_num="${cache_id#L#}"
    [[ "$cache_id_num" =~ ^[0-9]+$ ]] || continue
    [[ -n "$allocation_types" ]] || continue
    [[ "$size" =~ ^[0-9]+KB$ ]] || continue
    if ! cache_array_json="$({
      jq -c \
        --arg level "$level" \
        --arg cache_id "$cache_id_num" \
        --arg allocation_types "$allocation_types" \
        --arg size "$size" \
        '
          ($allocation_types
            | split(",")
            | map(gsub("^ +| +$"; "") | ascii_downcase)
            | map(select(. == "exclusive" or . == "shared"))
            | unique
          ) as $types
          |
          if ($types | length) == 0 then
            .
          elif any(.[]; .cacheId == $cache_id and .level == $level and .size == $size) then
            map(
              if .cacheId == $cache_id and .level == $level and .size == $size then
                .allocationTypes = ((.allocationTypes + $types) | unique)
              else
                .
              end
            )
          else
            . + [{level: $level, cacheId: $cache_id, allocationTypes: $types, size: $size}]
          end
        ' <<< "$cache_array_json"
    })"; then
      echo "ERROR: Failed to construct cache capabilities JSON"
      return 1
    fi
  done < <(grep -v '^#' "$cache_file")

  if ! cache_array_json="$(jq -c '
    map({
      level: .level,
      cacheId: (.cacheId | tostring | gsub("^L#"; "")),
      size: .size,
      allocationTypes: ((.allocationTypes | unique | sort))
    })
    | sort_by(.level, (.cacheId | tonumber), .size)
  ' <<< "$cache_array_json")"; then
    echo "ERROR: Failed to finalize cache capabilities JSON"
    return 1
  fi

  if [[ "$cache_array_json" == "[]" ]]; then
    echo "ERROR: Failed to construct cache capabilities JSON"
    return 1
  fi

  if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required to update capabilities.json"
    return 1
  fi

  local tmp_file
  tmp_file="$(mktemp)"

  if ! jq --argjson cache "$cache_array_json" '
    def legacy_cache_item_ok:
      (type == "object")
      and (.level | type == "string")
      and (.allocation | type == "string")
      and (.size | type == "string");

    def cache_item_ok:
      (type == "object")
      and (.level | type == "string")
      and (
        (has("cacheId") | not)
        or
        (
          ((.cacheId | type) == "string" or (.cacheId | type) == "number")
          and ((.cacheId | tostring | gsub("^L#"; "") | test("^[0-9]+$")))
        )
      )
      and (.allocationTypes | type == "array")
      and (.allocationTypes | length > 0)
      and (.allocationTypes | all(.[]; (type == "string") and (. == "exclusive" or . == "shared")))
      and (.size | type == "string");

    def normalize_cache_shape:
      if (type == "array") and (all(.[]; legacy_cache_item_ok)) then
        map({
          level: .level,
          cacheId: "0",
          allocationTypes: [.allocation],
          size: .size
        })
      elif (type == "array") and (all(.[]; cache_item_ok)) then
        to_entries
        | map({
            level: .value.level,
            cacheId: ((.value.cacheId // (.key | tostring)) | tostring | gsub("^L#"; "")),
            allocationTypes: ((.value.allocationTypes | unique | sort)),
            size: .value.size
          })
      else
        .
      end;

    def cache_shape_similar_at($path):
      (getpath($path) | type) == "object"
      and (
        ((getpath($path + ["cache"]) | type) == "array"
         and ((getpath($path + ["cache"]) | all(.[]; cache_item_ok))
              or (getpath($path + ["cache"]) | all(.[]; legacy_cache_item_ok))))
        or
        ((getpath($path + ["cache"]) | type) == "null")
      );

    if cache_shape_similar_at(["properties", "resources"]) then
      setpath(["properties", "resources", "cache"]; $cache)
    elif cache_shape_similar_at(["resources"]) then
      setpath(["resources", "cache"]; $cache)
    else
      error("Refusing cache update: existing properties.resources.cache/resources.cache schema is not similar to expected cache capability entries (level, allocationTypes[], size)")
    end
  ' "$capabilities_file" > "$tmp_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to update resources.cache in $capabilities_file"
    return 1
  fi

  if ! mv "$tmp_file" "$capabilities_file"; then
    rm -f "$tmp_file"
    echo "ERROR: Failed to persist updated capabilities file"
    return 1
  fi

  echo "Updated cache capabilities in $capabilities_file"
  return 0
}

update_capabilities_resources_from_host() {
  local capabilities_file="${1:-$HOME/sandbox/poc/device/agent/config/capabilities.json}"
  local cache_file="${2:-$CPU_TOPOLOGY_CACHE_FILE}"

  update_capabilities_cpu_from_host "$capabilities_file" "$cache_file" || return 1
  update_capabilities_cache_from_host "$capabilities_file" || return $?
  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  update_capabilities_resources_from_host "$@"
fi
