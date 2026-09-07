#!/bin/bash
# modules/cache-topology.sh - Cache topology and Intel RDT helpers
#
# Provides:
#   - Cache discovery from sysfs (with lstopo fallback)
#   - Cache way and allocation mode detection from resctrl info
#   - Cache topology TSV and JSON serialization
#
# Globals populated by cache discovery:
#   _CACHE_TOPOLOGY_INSTANCES[level|id|size_kib]=1
#   _CACHE_TOPOLOGY_WAYS[level|id|size_kib]=number of allocation ways
#   _CACHE_TOPOLOGY_WAY_SIZE_KIB[level|id|size_kib]=size of one way in KiB
#   _CACHE_TOPOLOGY_CORES[level|id|size_kib]=cores range list (e.g. 1-5,7-10)

if [[ -n "${_CACHE_TOPOLOGY_LOADED:-}" ]] && declare -F read_cache_topology_as_json >/dev/null; then
  return 0
fi
readonly _CACHE_TOPOLOGY_LOADED=1

CACHE_TOPOLOGY_CACHE_FILE="${CACHE_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cache-topology.tsv}"
CACHE_TOPOLOGY_DEBUG="${CACHE_TOPOLOGY_DEBUG:-0}"
CACHE_SYSFS_CPU_ROOT="${CACHE_SYSFS_CPU_ROOT:-/sys/devices/system/cpu}"
CACHE_RESCTRL_ROOT="${CACHE_RESCTRL_ROOT:-${RDT_RESCTRL_ROOT:-/sys/fs/resctrl}}"

_cache_topology_debug_enabled() {
  case "${CACHE_TOPOLOGY_DEBUG,,}" in
    1|true|yes|on) return 0 ;;
    *) return 1 ;;
  esac
}

_cache_topology_debug() {
  _cache_topology_debug_enabled || return 0
  echo "[DEBUG][cache-topology] $*" >&2
}

_read_trimmed_file() {
  local file_path="$1"
  [[ -r "$file_path" ]] || return 0
  tr -d '[:space:]' < "$file_path" 2>/dev/null || true
}

_cache_size_token_to_kib() {
  local size_token="$1"
  local value unit
  if [[ "$size_token" =~ ^([0-9]+)([[:space:]]*)([KMG](iB|i|B)?|B)$ ]]; then
    value="${BASH_REMATCH[1]}"
    unit="${BASH_REMATCH[3]}"
  else
    printf '0\n'
    return 0
  fi

  case "$unit" in
    K|KB|Ki|KiB) printf '%s\n' "$value" ;;
    M|MB|Mi|MiB) printf '%s\n' "$(( value * 1024 ))" ;;
    G|GB|Gi|GiB) printf '%s\n' "$(( value * 1024 * 1024 ))" ;;
    B)
      if (( value == 0 )); then
        printf '0\n'
      else
        printf '%s\n' "$(( (value + 1023) / 1024 ))"
      fi
      ;;
    *) printf '0\n' ;;
  esac
}

_collect_cache_instances_from_lstopo() {
  command -v lstopo >/dev/null 2>&1 || return 1

  local topology_output
  topology_output="$(lstopo --no-io 2>/dev/null)" || return 1

  local found=0
  local line cache_id size_token size_kib
  while IFS= read -r line; do
    if [[ "$line" =~ ^[[:space:]]*L3[[:space:]]+L#([0-9]+)[[:space:]]*\(([0-9]+[[:space:]]*[KMG]i?B|[0-9]+[[:space:]]*B)\) ]]; then
      cache_id="L#${BASH_REMATCH[1]}"
      size_token="${BASH_REMATCH[2]}"
      size_kib="$(_cache_size_token_to_kib "$size_token")"
      if [[ "$size_kib" =~ ^[0-9]+$ ]] && (( size_kib > 0 )); then
        # lstopo text output does not reliably provide range-form core lists.
        printf 'L3\t%s\t%s\t\n' "$cache_id" "$size_kib"
        found=1
      fi
    fi
  done <<< "$topology_output"

  (( found == 1 )) || return 1
}

_collect_cache_instances_from_sysfs() {
  local cpu_path index_path
  declare -A seen_instances=()
  declare -A fallback_id_by_fingerprint=()
  local next_fallback_id=0
  local emitted=0

  for cpu_path in "$CACHE_SYSFS_CPU_ROOT"/cpu[0-9]*; do
    [[ -d "$cpu_path" ]] || continue
    for index_path in "$cpu_path"/cache/index*; do
      [[ -d "$index_path" ]] || continue

      local level_num size_token shared_cpu_list size_kib cache_id_raw cache_id
      local cache_fingerprint instance_fingerprint
      level_num="$(_read_trimmed_file "$index_path/level")"
      [[ "$level_num" == 3 ]] || continue

      size_token="$(_read_trimmed_file "$index_path/size")"
      shared_cpu_list="$(_read_trimmed_file "$index_path/shared_cpu_list")"
      cache_id_raw="$(_read_trimmed_file "$index_path/id")"
      [[ -n "$shared_cpu_list" ]] || continue

      size_kib="$(_cache_size_token_to_kib "$size_token")"
      [[ "$size_kib" =~ ^[0-9]+$ ]] || continue
      (( size_kib > 0 )) || continue

      if [[ "$cache_id_raw" =~ ^[0-9]+$ ]]; then
        cache_id="L#${cache_id_raw}"
      else
        cache_fingerprint="L3|${size_kib}|${shared_cpu_list}"
        if [[ -z "${fallback_id_by_fingerprint[$cache_fingerprint]:-}" ]]; then
          fallback_id_by_fingerprint["$cache_fingerprint"]="L#${next_fallback_id}"
          next_fallback_id=$((next_fallback_id + 1))
        fi
        cache_id="${fallback_id_by_fingerprint[$cache_fingerprint]}"
      fi

      instance_fingerprint="L3|${cache_id}|${size_kib}|${shared_cpu_list}"
      if [[ -z "${seen_instances[$instance_fingerprint]:-}" ]]; then
        seen_instances["$instance_fingerprint"]=1
        printf 'L3\t%s\t%s\t%s\n' "$cache_id" "$size_kib" "$shared_cpu_list"
        _cache_topology_debug "sysfs discovered cache level=L3 id=${cache_id} size_kib=${size_kib} cores=${shared_cpu_list}"
        emitted=$((emitted + 1))
      fi
    done
  done

  _cache_topology_debug "sysfs discovery emitted ${emitted} unique L3 cache entries"
  (( emitted > 0 ))
}

# Discover live cache topology and populate maps keyed by level|id|size_kib.
_discover_cache_topology() {
  declare -gA _CACHE_TOPOLOGY_INSTANCES=()
  declare -gA _CACHE_TOPOLOGY_CORES=()

  local discovered_instances=""
  # Prefer sysfs for core range mapping (shared_cpu_list already in range format).
  discovered_instances="$(_collect_cache_instances_from_sysfs 2>/dev/null || true)"
  if [[ -n "$discovered_instances" ]]; then
    _cache_topology_debug "using sysfs cache discovery"
  else
    _cache_topology_debug "sysfs cache discovery returned no entries, falling back to lstopo"
    discovered_instances="$(_collect_cache_instances_from_lstopo 2>/dev/null || true)"
  fi

  [[ -n "$discovered_instances" ]] || return 1

  local level cache_id size_kib cores instance_key
  while IFS=$'\t' read -r level cache_id size_kib cores; do
    [[ "$level" == "L3" ]] || continue
    [[ "$cache_id" =~ ^L#[0-9]+$ ]] || continue
    [[ "$size_kib" =~ ^[0-9]+$ ]] || continue
    instance_key="${level}|${cache_id}|${size_kib}"
    _CACHE_TOPOLOGY_INSTANCES["$instance_key"]=1
    if [[ -n "$cores" ]]; then
      _CACHE_TOPOLOGY_CORES["$instance_key"]="$cores"
    fi
    _cache_topology_debug "parsed cache key=${instance_key} cores=${cores:-<empty>}"
  done <<< "$discovered_instances"

  # Populate _CACHE_TOPOLOGY_WAYS if resctrl is available
  declare -gA _CACHE_TOPOLOGY_WAYS=() _CACHE_TOPOLOGY_WAY_SIZE_KIB=()
  local ways
  ways="$(_get_cache_ways_for_level L3 2>/dev/null || true)"
  if [[ ! "$ways" =~ ^[0-9]+$ ]] || (( ways == 0 )); then
    echo "[ERROR] Intel RDT/CAT is not enabled properly: no L3 cache allocation ways were found" >&2
    return 1
  fi

  for instance_key in "${!_CACHE_TOPOLOGY_INSTANCES[@]}"; do
    local size_kib_from_key="${instance_key##*|}"
    if [[ "$size_kib_from_key" =~ ^[0-9]+$ ]] && (( size_kib_from_key > 0 )); then
      local way_size=$(( (size_kib_from_key + ways/2) / ways ))  # round to nearest
      _CACHE_TOPOLOGY_WAYS["$instance_key"]="$ways"
      _CACHE_TOPOLOGY_WAY_SIZE_KIB["$instance_key"]="$way_size"
    fi
  done

  [[ ${#_CACHE_TOPOLOGY_INSTANCES[@]} -gt 0 ]]
}

# Count the number of set bits in a hexadecimal string.
_count_bits_in_hex() {
  local hex="$1"
  hex="${hex#0x}"
  hex="${hex#0X}"
  [[ -z "$hex" ]] && echo 0 && return 0

  local count=0
  local i
  for (( i=0; i<${#hex}; i++ )); do
    local nibble="${hex:$i:1}"
    case "$nibble" in
      0) ;;
      1|2|4|8) ((count += 1)) ;;
      3|5|6|9|a|A) ((count += 2)) ;;
      7|b|B|d|D) ((count += 3)) ;;
      c|C|e|E) ((count += 2)) ;;
      f|F) ((count += 4)) ;;
      *) ;;
    esac
  done
  echo "$count"
}

# Get the number of cache ways for a level from resctrl cbm_mask.
_get_cache_ways_for_level() {
  local level="$1"
  local info_dir="$CACHE_RESCTRL_ROOT/info/${level}"
  [[ -r "$info_dir/cbm_mask" ]] || { echo 0; return 1; }

  local mask
  mask="$(tr -d '[:space:]' < "$info_dir/cbm_mask" 2>/dev/null || true)"
  [[ -z "$mask" ]] && { echo 0; return 1; }

  _count_bits_in_hex "$mask"
}

# Echo allocation modes for a level as a space-separated list.
# Levels with CAT support return "shared exclusive"; otherwise "shared".
_get_cache_allocation_modes_for_level() {
  local level="$1"

  case "$level" in
    L3)
      local ways
      if ways="$(_get_cache_ways_for_level "$level")" && (( ways > 0 )); then
        echo "shared exclusive"
        return 0
      fi
      echo "shared"
      ;;
    *)
      echo "shared"
      ;;
  esac
}

# Generate a complete cache topology TSV document for sorted level|numeric_id|size_kib keys.
# Writes the TSV to stdout and returns non-zero if any field cannot be generated or printed.
_generate_cache_topology_tsv() {
  local sorted_cache_level_id_size_keys="$1"
  local sorted_key level cache_id size_kib allocation_types allocation_modes
  local instance_key ways way_size_kib cores

  echo "# cache-topology cache — generated $(date -u '+%Y-%m-%dT%H:%M:%SZ') by $(uname -n)" || return 1
  printf '# %s\t%s\t%s\t%s\t%s\t%s\t%s\n' "level" "id" "allocationTypes" "size" "ways" "way_size_kib" "cores" || return 1

  while IFS= read -r sorted_key; do
    [[ -n "$sorted_key" ]] || continue
    IFS='|' read -r level cache_id size_kib <<< "$sorted_key"
    cache_id="L#${cache_id}"
    instance_key="${level}|${cache_id}|${size_kib}"

    ways="${_CACHE_TOPOLOGY_WAYS["$instance_key"]:-0}"
    way_size_kib="${_CACHE_TOPOLOGY_WAY_SIZE_KIB["$instance_key"]:-0}"
    cores="${_CACHE_TOPOLOGY_CORES["$instance_key"]:-}"

    allocation_modes="$(_get_cache_allocation_modes_for_level "$level")" || return 1
    allocation_types="${allocation_modes// /,}"
    printf '%s\t%s\t%s\t%sKi\t%s\t%s\t%s\n' "$level" "$cache_id" "$allocation_types" "$size_kib" "$ways" "$way_size_kib" "$cores" || return 1
  done <<< "$sorted_cache_level_id_size_keys"
}

# Build cache topology TSV cache file.
# Cache file format (tab-separated, one cache instance per line):
#   # comments / header lines starting with #
#   <level>  <id>  <allocationTypes>  <size>  <ways>  <way_size_kib>  <cores>
# allocationTypes is a comma-separated list (for example: "shared" or "shared,exclusive").
#
# Usage: build_cache_topology [output_file]
build_cache_topology() {
  if ! _discover_cache_topology; then
    echo "[ERROR] Unable to discover cache topology from sysfs or lstopo" >&2
    return 1
  fi

  local output_file="${1:-$CACHE_TOPOLOGY_CACHE_FILE}"
  local output_dir
  output_dir="$(dirname "$output_file")"
  if ! mkdir -p "$output_dir"; then
    echo "[ERROR] Failed to create cache topology directory: $output_dir" >&2
    return 1
  fi

  _cache_topology_debug "writing cache topology to ${output_file}"

  local tmp_file
  if ! tmp_file="$(mktemp "$output_dir/.cache-topology.XXXXXX")"; then
    echo "[ERROR] Failed to create a temporary cache topology file in: $output_dir" >&2
    return 1
  fi

  local sorted_cache_level_id_size_keys
  sorted_cache_level_id_size_keys="$(printf '%s\n' "${!_CACHE_TOPOLOGY_INSTANCES[@]}" | awk -F'|' '
    {
      idnum = $2;
      sub(/^L#/, "", idnum);
      print $1 "|" idnum "|" $3
    }
  ' | sort -t'|' -k1,1 -k2,2n -k3,3n)"

  if ! _generate_cache_topology_tsv "$sorted_cache_level_id_size_keys" > "$tmp_file"; then
    rm -f "$tmp_file"
    echo "[ERROR] Failed to write cache topology cache: $tmp_file" >&2
    return 1
  fi

  if [[ -e "$output_file" ]] && ! chmod --reference="$output_file" "$tmp_file"; then
    rm -f "$tmp_file"
    echo "[ERROR] Failed to preserve permissions for cache topology cache: $output_file" >&2
    return 1
  fi

  if ! mv -f "$tmp_file" "$output_file"; then
    rm -f "$tmp_file"
    echo "[ERROR] Failed to persist cache topology cache: $output_file" >&2
    return 1
  fi

  local written_lines=0
  written_lines="$(wc -l < "$output_file" 2>/dev/null || echo 0)"
  _cache_topology_debug "wrote ${written_lines} lines to ${output_file}"

  echo "[INFO] Cache topology cached: $output_file"
}

# Print the persisted cache topology as a JSON array.
# Usage: read_cache_topology_as_json [cache_file]
read_cache_topology_as_json() {
  local cache_file="${1:-$CACHE_TOPOLOGY_CACHE_FILE}"

  if ! command -v jq >/dev/null 2>&1; then
    echo "[ERROR] jq is required to serialize cache topology" >&2
    return 1
  fi
  if [[ ! -f "$cache_file" ]]; then
    echo "[ERROR] Cache topology file does not exist: $cache_file" >&2
    return 1
  fi
  if [[ ! -r "$cache_file" ]]; then
    echo "[ERROR] Cache topology file is unreadable: $cache_file" >&2
    return 1
  fi

  local topology_json='[]'
  local line_number=0
  local record_count=0
  local level cache_id allocation_types size_token ways way_size_kib cores
  local size_kib instance_key
  declare -A seen_instances=()
  while IFS=$'\t' read -r level cache_id allocation_types size_token ways way_size_kib cores; do
    line_number=$((line_number + 1))
    [[ "$level" == "#"* || -z "$level" ]] && continue

    size_kib="$(_cache_size_token_to_kib "$size_token")"
    if [[ ! "$level" =~ ^L[0-9]+$ || ! "$cache_id" =~ ^L#[0-9]+$ ||
      ! "$allocation_types" =~ ^[a-z]+(,[a-z]+)*$ ||
      ! "$size_kib" =~ ^[0-9]+$ || "$size_kib" == 0 ||
      ! "$ways" =~ ^[0-9]+$ || ! "$way_size_kib" =~ ^[0-9]+$ ]]; then
      echo "[ERROR] Invalid cache topology record at $cache_file:$line_number" >&2
      return 1
    fi

    instance_key="${level}|${cache_id}|${size_kib}"
    if [[ -n "${seen_instances[$instance_key]:-}" ]]; then
      echo "[ERROR] Duplicate cache topology record at $cache_file:$line_number" >&2
      return 1
    fi
    seen_instances["$instance_key"]=1

    topology_json="$(jq -c \
      --arg level "$level" \
      --arg id "$cache_id" \
      --arg allocation_types "$allocation_types" \
      --argjson size_kib "$size_kib" \
      --argjson ways "$ways" \
      --argjson way_size_kib "$way_size_kib" \
      --arg cores "$cores" \
      '. + [{
        level: $level,
        id: $id,
        allocationTypes: ($allocation_types | split(",")),
        sizeKiB: $size_kib,
        ways: $ways,
        waySizeKiB: $way_size_kib,
        cores: $cores
      }]' <<< "$topology_json")" || return 1
    record_count=$((record_count + 1))
  done < "$cache_file"

  if (( record_count == 0 )); then
    echo "[ERROR] Cache topology file contains no valid records: $cache_file" >&2
    return 1
  fi

  printf '%s\n' "$topology_json"
}

# Get the number of classes of service a level exposes, or 0 when unavailable.
_get_num_closids_for_level() {
  local level="$1"
  local closid_file="$CACHE_RESCTRL_ROOT/info/${level}/num_closids"
  [[ -r "$closid_file" ]] || { echo 0; return 1; }

  local value
  value="$(tr -d '[:space:]' < "$closid_file" 2>/dev/null || true)"
  [[ "$value" =~ ^[0-9]+$ ]] || { echo 0; return 1; }

  echo "$value"
}

# Echo the most classes of service the device can hold: the minimum num_closids across
# the resctrl levels present, because one control group consumes a CLOS in every level.
# Echoes 0 when resctrl is unmounted or exposes no level.
get_device_max_closids() {
  local min=0
  local info_dir level value

  for info_dir in "$CACHE_RESCTRL_ROOT"/info/*/; do
    [[ -d "$info_dir" ]] || continue
    level="$(basename "$info_dir")"
    value="$(_get_num_closids_for_level "$level")" || continue
    (( value > 0 )) || continue
    if (( min == 0 || value < min )); then
      min="$value"
    fi
  done

  if (( min == 0 )); then
    echo "[WARN] resctrl exposes no num_closids; cache allocation might not function properly" >&2
  fi

  _cache_topology_debug "device max_closids resolved to ${min}"
  echo "$min"
}
