#!/bin/bash
# modules/cache-topology.sh - Cache topology and Intel RDT helpers
#
# Provides:
#   - Intel RDT capability/readiness checks
#   - Cache discovery from lstopo (with sysfs fallback)
#   - Cache allocation mode detection from resctrl info
#
# Globals populated by read_cache_topology:
#   _CACHE_TOPOLOGY_COUNTS[level|id|size_kb]=count
#   _CACHE_TOPOLOGY_CORES[level|id|size_kb]=cores range list (e.g. 1-5,7-10)

[[ -n "${_CACHE_TOPOLOGY_LOADED:-}" ]] && return 0
export _CACHE_TOPOLOGY_LOADED=1

CACHE_TOPOLOGY_CACHE_FILE="${CACHE_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cache-topology.tsv}"
CACHE_TOPOLOGY_DEBUG="${CACHE_TOPOLOGY_DEBUG:-0}"

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

# Return 0 if host looks Intel and advertises RDT-related CPU flags.
is_intel_rdt_capable_host() {
  local vendor flags
  vendor="$(awk -F: '/^vendor_id/{gsub(/^ +/, "", $2); print $2; exit}' /proc/cpuinfo 2>/dev/null || true)"
  [[ "$vendor" == "GenuineIntel" ]] || return 1

  flags="$(awk -F: '/^flags/{gsub(/^ +/, "", $2); print $2; exit}' /proc/cpuinfo 2>/dev/null || true)"
  [[ "$flags" =~ (^|[[:space:]])(cat_l3|cat_l2|cqm|mba)([[:space:]]|$) ]]
}

# Return 0 if cmdline and resctrl indicate Intel RDT is enabled and usable now.
is_intel_rdt_enabled_and_usable() {
  local cmdline
  cmdline="$(cat /proc/cmdline 2>/dev/null || true)"

  [[ "$cmdline" == *"intel_rdt=on"* ]] || return 1
  [[ "$cmdline" == *"rdt="* ]] || return 1

  [[ -d /sys/fs/resctrl ]] || return 1

  # At least one CAT info directory must be visible once resctrl is mounted.
  if [[ -d /sys/fs/resctrl/info/L3 || -d /sys/fs/resctrl/info/L2 ]]; then
    return 0
  fi
  return 1
}

ensure_resctrl_mounted() {
  if [[ -d /sys/fs/resctrl/info ]]; then
    return 0
  fi

  if ! command -v sudo >/dev/null 2>&1; then
    return 1
  fi

  sudo mkdir -p /sys/fs/resctrl || return 1
  sudo mount -t resctrl resctrl /sys/fs/resctrl || return 1

  [[ -d /sys/fs/resctrl/info ]]
}

print_intel_rdt_enable_instructions() {
  local rdt_params="intel_rdt=on rdt=cmt,mbmtotal,mbmlocal,l3cat,l3cdp,l2cat,l2cdp,mba"

  echo "[ACTION REQUIRED] Intel RDT capabilities detected, but resctrl/CAT is not fully enabled."
  echo "[ACTION REQUIRED] Enable Intel RDT and mount resctrl, then rerun this script."
  echo ""
  echo "Suggested commands (based on Intel CAT wiki guidance):"
  echo "To make it persistent via GRUB (Ubuntu/Debian style):"
  echo "  sudo cp /etc/default/grub /etc/default/grub.bak.$(date +%Y%m%d%H%M%S)"
  echo "  sudo sed -i '/^GRUB_CMDLINE_LINUX=/ s|\"$| ${rdt_params}\"|' /etc/default/grub"
  echo "  cat /etc/default/grub" # to be sure we're appending the rdt_params
  echo "  sudo update-grub"
  echo "  sudo reboot"
}

_cache_size_token_to_kb() {
  local raw="$1"
  local value unit
  if [[ "$raw" =~ ^([0-9]+)([[:space:]]*)([KMG]i?B|[KMG]B|[KMG]|B)$ ]]; then
    value="${BASH_REMATCH[1]}"
    unit="${BASH_REMATCH[3]}"
  else
    echo "0"
    return 0
  fi

  case "$unit" in
    K|KB|KiB) echo "$value" ;;
    M|MB|MiB) echo "$(( value * 1024 ))" ;;
    G|GB|GiB) echo "$(( value * 1024 * 1024 ))" ;;
    B)
      if (( value == 0 )); then echo "0"; else echo "$(( (value + 1023) / 1024 ))"; fi
      ;;
    *) echo "0" ;;
  esac
}

_collect_cache_instances_from_lstopo() {
  command -v lstopo >/dev/null 2>&1 || return 1

  local out
  out="$(lstopo --no-io 2>/dev/null)" || return 1

  local found=0
  local line cache_id size_token size_kb
  while IFS= read -r line; do
    if [[ "$line" =~ ^[[:space:]]*L3[[:space:]]+L#([0-9]+)[[:space:]]*\(([0-9]+[[:space:]]*[KMG]i?B|[0-9]+[[:space:]]*B)\) ]]; then
      cache_id="L#${BASH_REMATCH[1]}"
      size_token="${BASH_REMATCH[2]}"
      size_kb="$(_cache_size_token_to_kb "$size_token")"
      if [[ "$size_kb" =~ ^[0-9]+$ ]] && (( size_kb > 0 )); then
        # lstopo text output does not reliably provide range-form core lists.
        echo -e "L3\t${cache_id}\t${size_kb}\t"
        found=1
      fi
    fi
  done <<< "$out"

  (( found == 1 )) || return 1
}

_collect_cache_instances_from_sysfs() {
  local cpu_path index_path
  declare -A seen=()
  local instance_index=0
  local emitted=0

  for cpu_path in /sys/devices/system/cpu/cpu[0-9]*; do
    [[ -d "$cpu_path" ]] || continue
    for index_path in "$cpu_path"/cache/index*; do
      [[ -d "$index_path" ]] || continue

      local level_num cache_type size_raw shared_list level_name size_kb uniq cache_id_raw cache_id
      level_num="$(cat "$index_path/level" 2>/dev/null || true)"
      cache_type="$(cat "$index_path/type" 2>/dev/null || true)"
      size_raw="$(cat "$index_path/size" 2>/dev/null || true)"
      shared_list="$(tr -d '[:space:]' < "$index_path/shared_cpu_list" 2>/dev/null || true)"
      cache_id_raw="$(cat "$index_path/id" 2>/dev/null || true)"

      case "$level_num:$cache_type" in
        3:*)          level_name="L3" ;;
        *)            continue ;;
      esac

      size_kb="$(_cache_size_token_to_kb "$size_raw")"
      [[ "$size_kb" =~ ^[0-9]+$ ]] || continue
      (( size_kb > 0 )) || continue

      if [[ "$cache_id_raw" =~ ^[0-9]+$ ]]; then
        cache_id="L#${cache_id_raw}"
      else
        cache_id="L#${instance_index}"
      fi

      [[ -n "$shared_list" ]] || continue

      uniq="${level_name}|${cache_id}|${size_kb}|${shared_list}"
      if [[ -z "${seen[$uniq]:-}" ]]; then
        seen["$uniq"]=1
        echo -e "${level_name}\t${cache_id}\t${size_kb}\t${shared_list}"
        _cache_topology_debug "sysfs discovered cache level=${level_name} id=${cache_id} size_kb=${size_kb} cores=${shared_list}"
        emitted=$((emitted + 1))
        instance_index=$((instance_index + 1))
      fi
    done
  done

  _cache_topology_debug "sysfs discovery emitted ${emitted} unique L3 cache entries"
}

# Populate _CACHE_TOPOLOGY_COUNTS with key level|size_kb and integer count.
read_cache_topology() {
  declare -gA _CACHE_TOPOLOGY_COUNTS=()
  declare -gA _CACHE_TOPOLOGY_CORES=()

  local raw_instances=""
  # Prefer sysfs for core range mapping (shared_cpu_list already in range format).
  if raw_instances="$(_collect_cache_instances_from_sysfs 2>/dev/null)"; then
    _cache_topology_debug "using sysfs cache discovery"
  else
    _cache_topology_debug "sysfs cache discovery failed, falling back to lstopo"
    raw_instances="$(_collect_cache_instances_from_lstopo 2>/dev/null || true)"
  fi

  [[ -n "$raw_instances" ]] || return 1

  local raw_count=0
  raw_count="$(printf '%s\n' "$raw_instances" | awk 'NF>0{count++} END{print count+0}')"
  _cache_topology_debug "raw cache discovery produced ${raw_count} rows"

  local level cache_id size_kb cores
  while IFS=$'\t' read -r level cache_id size_kb cores; do
    [[ "$level" == "L3" ]] || continue
    [[ "$cache_id" =~ ^L#[0-9]+$ ]] || continue
    [[ "$size_kb" =~ ^[0-9]+$ ]] || continue
    local key="${level}|${cache_id}|${size_kb}"
    _CACHE_TOPOLOGY_COUNTS["$key"]=$(( ${_CACHE_TOPOLOGY_COUNTS["$key"]:-0} + 1 ))
    if [[ -n "$cores" ]]; then
      _CACHE_TOPOLOGY_CORES["$key"]="$cores"
    fi
    _cache_topology_debug "parsed cache key=${key} cores=${cores:-<empty>}"
  done <<< "$raw_instances"

  # Populate _CACHE_TOPOLOGY_WAYS if resctrl is available
  declare -gA _CACHE_TOPOLOGY_WAYS=() _CACHE_TOPOLOGY_WAY_SIZE=()
  local ways
  ways="$(get_cache_ways_for_level L3 2>/dev/null || echo 0)"
  if [[ "$ways" =~ ^[0-9]+$ ]] && (( ways > 0 )); then
    for key in "${!_CACHE_TOPOLOGY_COUNTS[@]}"; do
      local size_kb_from_key="${key##*|}"
      if [[ "$size_kb_from_key" =~ ^[0-9]+$ ]] && (( size_kb_from_key > 0 )); then
        local way_size=$(( (size_kb_from_key + ways/2) / ways ))  # round to nearest
        _CACHE_TOPOLOGY_WAYS["$key"]="$ways"
        _CACHE_TOPOLOGY_WAY_SIZE["$key"]="$way_size"
      fi
    done
  fi

  [[ ${#_CACHE_TOPOLOGY_COUNTS[@]} -gt 0 ]]
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
get_cache_ways_for_level() {
  local level="$1"
  local info_dir="/sys/fs/resctrl/info/${level}"
  [[ -r "$info_dir/cbm_mask" ]] || { echo 0; return 1; }

  local mask
  mask="$(tr -d '[:space:]' < "$info_dir/cbm_mask" 2>/dev/null || true)"
  [[ -z "$mask" ]] && { echo 0; return 1; }

  _count_bits_in_hex "$mask"
}

# Echo allocation modes for a level as a space-separated list.
# Levels with CAT support return "shared exclusive"; otherwise "shared".
get_cache_allocation_modes_for_level() {
  local level="$1"

  case "$level" in
    L3)
      local info_dir="/sys/fs/resctrl/info/${level}"
      if [[ -d "$info_dir" ]]; then
        local has_cbm=0
        [[ -s "$info_dir/cbm_mask" ]] && has_cbm=1
        if (( has_cbm == 1 )); then
          echo "shared exclusive"
          return 0
        fi
      fi
      echo "shared"
      ;;
    *)
      echo "shared"
      ;;
  esac
}

# Build cache topology TSV cache file.
# Cache file format (tab-separated, one cache instance per line):
#   # comments / header lines starting with #
#   <level>  <id>  <allocationTypes>  <size>  <ways>  <way_size_kb>  <cores>
# allocationTypes is a comma-separated list (for example: "shared" or "shared,exclusive").
#
# Usage: build_cache_topology_cache [output_file]
build_cache_topology_cache() {
  if ! read_cache_topology; then
    echo "[ERROR] Unable to discover cache topology from lstopo/sysfs" >&2
    return 1
  fi

  local output_file="${1:-$CACHE_TOPOLOGY_CACHE_FILE}"
  mkdir -p "$(dirname "$output_file")"

  _cache_topology_debug "writing cache topology to ${output_file}"
  if [[ -e "$output_file" && ! -w "$output_file" ]]; then
    _cache_topology_debug "output path exists but is not writable: ${output_file}"
  fi

  local keys_sorted
  keys_sorted="$(printf '%s\n' "${!_CACHE_TOPOLOGY_COUNTS[@]}" | awk -F'|' '
    {
      idnum = $2;
      sub(/^L#/, "", idnum);
      print 1 "|" $1 "|" idnum "|" $3
    }
  ' | sort -t'|' -k1,1n -k3,3n -k4,4n)"

  if ! {
    echo "# cache-topology cache — generated $(date -u '+%Y-%m-%dT%H:%M:%SZ') by $(uname -n)"
    printf '# %s\t%s\t%s\t%s\t%s\t%s\t%s\n' "level" "id" "allocationTypes" "size" "ways" "way_size_kb" "cores"

    local line ord level cache_id size_kb count allocation_types modes i
    while IFS= read -r line; do
      [[ -n "$line" ]] || continue
      IFS='|' read -r ord level cache_id size_kb <<< "$line"
      cache_id="L#${cache_id}"
      local key="${level}|${cache_id}|${size_kb}"
      count="${_CACHE_TOPOLOGY_COUNTS["$key"]:-0}"
      (( count > 0 )) || continue

      local ways="${_CACHE_TOPOLOGY_WAYS["$key"]:-0}"
      local way_size="${_CACHE_TOPOLOGY_WAY_SIZE["$key"]:-0}"
      local cores="${_CACHE_TOPOLOGY_CORES["$key"]:-}"

      modes="$(get_cache_allocation_modes_for_level "$level")"
      allocation_types="${modes// /,}"
      for ((i = 0; i < count; i++)); do
        printf '%s\t%s\t%s\t%sKB\t%s\t%s\t%s\n' "$level" "$cache_id" "$allocation_types" "$size_kb" "$ways" "$way_size" "$cores"
      done
    done <<< "$keys_sorted"
  } > "$output_file"; then
    echo "[ERROR] Failed to write cache topology cache: $output_file" >&2
    return 1
  fi

  local written_lines=0
  written_lines="$(wc -l < "$output_file" 2>/dev/null || echo 0)"
  _cache_topology_debug "wrote ${written_lines} lines to ${output_file}"

  echo "[INFO] Cache topology cached: $output_file"
}
