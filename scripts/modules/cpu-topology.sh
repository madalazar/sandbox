#!/bin/bash
# modules/cpu-topology.sh - Shared CPU topology helpers
#
# Provides three layers:
#   1. Pure helpers  — arch mapping, cpuset parsing, freq classification
#   2. Cache builder — build_cpu_topology_cache: full sysfs inspection → TSV file
#   3. Cache reader  — read_cpu_topology_cache:  TSV → globals used by consumers
#
# Cache file format (tab-separated, one physical core per line):
#   # comments / header lines starting with #
#   <core_id>  <arch>  <class>  <type>
#
# Globals populated by read_cpu_topology_cache:
#   _TOPO_SORTED_IDS  — indexed array of core IDs (numerically sorted)
#   _TOPO_CORE_META   — associative array: [id]="arch|class|type"
#
# Sourcing is idempotent (load-guard at the top).

[[ -n "${_CPU_TOPOLOGY_LOADED:-}" ]] && return 0
export _CPU_TOPOLOGY_LOADED=1

CPU_TOPOLOGY_CACHE_FILE="${CPU_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cpu-topology.tsv}"

# ---------------------------------------------------------------------------
# 1. Pure helpers
# ---------------------------------------------------------------------------

# Map uname -m output to the Margo API architecture value.
map_machine_arch_to_capability_arch() {
  local machine_arch="$1"
  case "$machine_arch" in
    x86_64|amd64)                    echo "amd64" ;;
    aarch64|arm64)                   echo "arm64" ;;
    armv7l|armv7*|armv6l|armv6*|arm) echo "arm"  ;;
    *)                               echo "amd64" ;;
  esac
}

# Populate an associative array with every CPU id in a range-list string
# (e.g. "0-3,7,10-11").
# Usage: mark_cpu_set_from_range_list "$range_str" my_assoc_array
mark_cpu_set_from_range_list() {
  local range_list="$1"
  local -n cpu_set_ref="$2"
  local part

  [[ -z "$range_list" ]] && return 0

  IFS=',' read -ra part_list <<<"$range_list"
  for part in "${part_list[@]}"; do
    if [[ "$part" =~ ^([0-9]+)-([0-9]+)$ ]]; then
      local start="${BASH_REMATCH[1]}" end="${BASH_REMATCH[2]}" cpu
      for ((cpu=start; cpu<=end; cpu++)); do cpu_set_ref["$cpu"]=1; done
    elif [[ "$part" =~ ^[0-9]+$ ]]; then
      cpu_set_ref["$part"]=1
    fi
  done
}

# Return the frequency tier for a given freq value given a sorted-descending
# array of unique frequencies.
# Usage: classify_cpu_frequency_tier "$freq" sorted_freqs_array_name
classify_cpu_frequency_tier() {
  local freq="$1"
  local -n sorted_freq_ref="$2"

  local tiers_count="${#sorted_freq_ref[@]}"
  if ((tiers_count <= 1)); then
    echo "performance"
    return 0
  fi

  local highest="${sorted_freq_ref[0]}"
  local lowest="${sorted_freq_ref[$((tiers_count - 1))]}"

  if   [[ "$freq" == "$highest" ]]; then echo "performance"
  elif [[ "$freq" == "$lowest"  ]]; then ((tiers_count >= 3)) && echo "low-power" || echo "efficiency"
  else echo "efficiency"
  fi
}

# ---------------------------------------------------------------------------
# 2. Cache builder
# ---------------------------------------------------------------------------

# Inspect the host's physical CPU topology and write a TSV cache file.
# HT siblings are collapsed — only one representative per physical core.
# Usage: build_cpu_topology_cache [output_file]
build_cpu_topology_cache() {
  local output_file="${1:-$CPU_TOPOLOGY_CACHE_FILE}"
  mkdir -p "$(dirname "$output_file")"

  local arch
  arch="$(map_machine_arch_to_capability_arch "$(uname -m)")"

  # ---- isolated CPU set --------------------------------------------------
  local isolated_range=""
  [[ -r /sys/devices/system/cpu/isolated ]] && \
    isolated_range="$(tr -d '[:space:]' </sys/devices/system/cpu/isolated 2>/dev/null || true)"
  declare -A _btc_isolated=()
  mark_cpu_set_from_range_list "$isolated_range" _btc_isolated

  # ---- enumerate physical cores (collapse HT siblings) -------------------
  declare -A _btc_seen=()
  local -a physical_ids=()
  local cpu_path
  for cpu_path in /sys/devices/system/cpu/cpu[0-9]*; do
    [[ -d "$cpu_path" ]] || continue
    local cpu_name="${cpu_path##*/}"
    local cpu_idx="${cpu_name#cpu}"
    [[ "$cpu_idx" =~ ^[0-9]+$ ]] || continue

    local siblings=""
    local siblings_file="$cpu_path/topology/thread_siblings_list"
    [[ -r "$siblings_file" ]] && \
      siblings="$(tr -d '[:space:]' <"$siblings_file" 2>/dev/null || true)"

    local rep="$cpu_idx"
    if [[ -n "$siblings" ]]; then
      local first="${siblings%%,*}"
      if   [[ "$first" =~ ^([0-9]+)-([0-9]+)$ ]]; then rep="${BASH_REMATCH[1]}"
      elif [[ "$first" =~ ^[0-9]+$             ]]; then rep="$first"
      fi
    fi
    [[ -z "${_btc_seen[$rep]:-}" ]] && { _btc_seen["$rep"]=1; physical_ids+=("$rep"); }
  done

  if [[ "${#physical_ids[@]}" -eq 0 ]]; then
    local total; total="$(nproc --all 2>/dev/null || echo 1)"
    local i; for ((i=0; i<total; i++)); do physical_ids+=("$i"); done
  fi

  # Sort numerically
  local -a sorted_ids=()
  while IFS= read -r id; do sorted_ids+=("$id"); done \
    < <(printf '%s\n' "${physical_ids[@]}" | sort -n)

  # ---- frequency classification ------------------------------------------
  declare -A _btc_freq_count=() _btc_freq_map=()
  local cpu_id
  for cpu_id in "${sorted_ids[@]}"; do
    local freq_file="/sys/devices/system/cpu/cpu${cpu_id}/cpufreq/cpuinfo_max_freq"
    local freq="0"
    if [[ -r "$freq_file" ]]; then
      freq="$(tr -d '[:space:]' <"$freq_file" 2>/dev/null || true)"
      [[ "$freq" =~ ^[0-9]+$ ]] || freq="0"
    fi
    _btc_freq_map["$cpu_id"]="$freq"
    _btc_freq_count["$freq"]=$(( ${_btc_freq_count["$freq"]:-0} + 1 ))
  done

  local -a _btc_sorted_freqs=()
  while IFS= read -r f; do [[ -n "$f" ]] && _btc_sorted_freqs+=("$f"); done \
    < <(printf '%s\n' "${!_btc_freq_count[@]}" | sort -nr)

  # ---- write TSV ---------------------------------------------------------
  {
    echo "# cpu-topology cache — generated $(date -u '+%Y-%m-%dT%H:%M:%SZ') by $(uname -n)"
    printf '# %s\t%s\t%s\t%s\n' "core_id" "arch" "class" "type"
    for cpu_id in "${sorted_ids[@]}"; do
      local ctype="shared"
      [[ -n "${_btc_isolated[$cpu_id]:-}" ]] && ctype="isolated"
      local class
      class="$(classify_cpu_frequency_tier "${_btc_freq_map[$cpu_id]}" _btc_sorted_freqs)"
      printf '%s\t%s\t%s\t%s\n' "$cpu_id" "$arch" "$class" "$ctype"
    done
  } > "$output_file"

  echo "[INFO] CPU topology cached: $output_file (${#sorted_ids[@]} physical cores)"
}

# ---------------------------------------------------------------------------
# 3. Cache reader
# ---------------------------------------------------------------------------

# Read the TSV cache into globals _TOPO_SORTED_IDS and _TOPO_CORE_META.
# Builds the cache first if the file does not exist.
# Usage: read_cpu_topology_cache [cache_file]
read_cpu_topology_cache() {
  local cache_file="${1:-$CPU_TOPOLOGY_CACHE_FILE}"

  if [[ ! -f "$cache_file" ]]; then
    build_cpu_topology_cache "$cache_file"
  fi

  declare -ga _TOPO_SORTED_IDS=()
  declare -gA _TOPO_CORE_META=()

  local id arch class type
  while IFS=$'\t' read -r id arch class type; do
    [[ "$id" =~ ^[0-9]+$ ]] || continue          # skip comment/header lines
    _TOPO_SORTED_IDS+=("$id")
    _TOPO_CORE_META["$id"]="${arch}|${class}|${type}"
  done < "$cache_file"
}

