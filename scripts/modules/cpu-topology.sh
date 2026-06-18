#!/bin/bash
# modules/cpu-topology.sh - Shared CPU topology helpers
#
# Provides three layers:
#   1. Pure helpers  — arch mapping, cpuset parsing, freq classification, lstopo classification
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

is_isolated_core_type() {
  local core_type="$1"
  core_type="${core_type,,}"
  [[ "$core_type" == "isolated" ]]
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

# Return the isolcpus range extracted from /proc/cmdline, if present.
# Supports forms like:
#   isolcpus=1,3
#   isolcpus=domain,managed_irq,1-3
get_isolcpus_range_from_proc_cmdline() {
  local cmdline
  cmdline="$(cat /proc/cmdline 2>/dev/null || true)"
  [[ -z "$cmdline" ]] && return 0

  local token
  for token in $cmdline; do
    if [[ "$token" == isolcpus=* ]]; then
      echo "${token#isolcpus=}"
      return 0
    fi
  done
}

# Return current cgroup cpuset range for this process.
# Prefers cpuset.cpus.effective, falls back to cpuset.cpus.
get_current_cgroup_cpuset_range() {
  local rel="/"
  local line
  while IFS= read -r line; do
    # cgroup v2 format: 0::/path
    if [[ "$line" =~ ^0::(.*)$ ]]; then
      rel="${BASH_REMATCH[1]}"
      [[ -z "$rel" ]] && rel="/"
      break
    fi
  done < /proc/self/cgroup

  local base="/sys/fs/cgroup"
  local dir="$base"
  if [[ "$rel" != "/" ]]; then
    dir="$base$rel"
  fi

  local cpuset_range=""
  local walk_dir="$dir"
  while true; do
    if [[ -r "$walk_dir/cpuset.cpus.effective" ]]; then
      cpuset_range="$(tr -d '[:space:]' <"$walk_dir/cpuset.cpus.effective" 2>/dev/null || true)"
    fi
    if [[ -z "$cpuset_range" && -r "$walk_dir/cpuset.cpus" ]]; then
      cpuset_range="$(tr -d '[:space:]' <"$walk_dir/cpuset.cpus" 2>/dev/null || true)"
    fi

    [[ -n "$cpuset_range" ]] && break
    [[ "$walk_dir" == "$base" ]] && break

    walk_dir="$(dirname "$walk_dir")"
    # Guard against escaping cgroup root due to unexpected paths.
    [[ "$walk_dir" == "/" ]] && break
    [[ "$walk_dir" != "$base" && "$walk_dir" != "$base"/* ]] && break
  done

  echo "$cpuset_range"
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

# Classify physical CPU cores by querying lstopo -v (hwloc).
#
# Reads "CPU kind #N efficiency E cpuset 0x..." blocks from lstopo output and
# maps each CPU ID to a Margo class string (performance / efficiency / low-power).
#
# Classification rules (per CPU kind, sorted by efficiency value):
#   Intel CoreType="IntelCore"             → performance
#   Intel CoreType="IntelAtom"  (top Atom) → efficiency
#   Intel CoreType="IntelAtom"  (low Atom, only when ≥3 kinds) → low-power
#   Generic: highest efficiency            → performance
#   Generic: lowest efficiency (≥3 kinds)  → low-power
#   Generic: lowest efficiency (2 kinds)   → efficiency
#   Generic: middle efficiency             → efficiency
#
# Usage: classify_cores_via_lstopo result_assoc_array_name
# Returns 0 on success, 1 if lstopo is unavailable or output is unparseable.
classify_cores_via_lstopo() {
  local -n _lcr="$1"   # caller's associative array: cpu_id → class

  command -v lstopo >/dev/null 2>&1 || return 1

  local lstopo_out
  lstopo_out="$(lstopo -v --no-io 2>/dev/null)" || return 1

  # --- Phase 1: collect CPU kind metadata ---------------------------------
  declare -A _lk_eff=() _lk_cpuset=() _lk_coretype=()
  local current_kind="" in_kind=0

  while IFS= read -r line; do
    # Match: "CPU kind #N efficiency E cpuset 0x..."
    if [[ "$line" =~ ^CPU[[:space:]]kind[[:space:]]#([0-9]+)[[:space:]]efficiency[[:space:]]([0-9]+)[[:space:]]cpuset[[:space:]]([^[:space:]]+) ]]; then
      current_kind="${BASH_REMATCH[1]}"
      _lk_eff["$current_kind"]="${BASH_REMATCH[2]}"
      _lk_cpuset["$current_kind"]="${BASH_REMATCH[3]}"
      _lk_coretype["$current_kind"]=""
      in_kind=1
    # Match: indented "CoreType = "IntelCore"" or "info CoreType = "IntelCore""
    elif [[ $in_kind -eq 1 && "$line" =~ CoreType[[:space:]]*=[[:space:]]*\"([^\"]+)\" ]]; then
      _lk_coretype["$current_kind"]="${BASH_REMATCH[1]}"
    # Non-indented line that is not a CPU kind line → exit kind context
    elif [[ $in_kind -eq 1 && -n "$line" && ! "$line" =~ ^[[:space:]] ]]; then
      in_kind=0; current_kind=""
    fi
  done <<< "$lstopo_out"

  local num_kinds=${#_lk_eff[@]}
  [[ $num_kinds -eq 0 ]] && return 1

  # --- Phase 2: determine class per kind ----------------------------------
  local max_eff=0 min_eff=999999 k e
  for k in "${!_lk_eff[@]}"; do
    e="${_lk_eff[$k]}"
    if (( e > max_eff )); then max_eff=$e; fi
    if (( e < min_eff )); then min_eff=$e; fi
  done

  declare -A _lk_class=()
  for k in "${!_lk_eff[@]}"; do
    e="${_lk_eff[$k]}"
    local ct="${_lk_coretype[$k]}" class
    if   [[ "$ct" == "IntelCore" ]]; then
      class="performance"
    elif [[ "$ct" == "IntelAtom" ]]; then
      # LP E-cores only exist when Intel exposes ≥3 kinds; they land at min efficiency
      if (( num_kinds >= 3 && e == min_eff )); then class="low-power"
      else class="efficiency"
      fi
    elif (( e == max_eff )); then
      class="performance"
    elif (( num_kinds >= 3 && e == min_eff )); then
      class="low-power"
    else
      class="efficiency"
    fi
    _lk_class["$k"]="$class"
  done

  # --- Phase 3: expand cpusets → cpu_id → class ---------------------------
  # Cpuset format: comma-separated hex 32-bit words, most-significant first.
  # Empty segments between commas represent zero words.
  local found_any=0
  for k in "${!_lk_cpuset[@]}"; do
    local cpuset="${_lk_cpuset[$k]}" cls="${_lk_class[$k]}"
    local -a raw_words=()
    IFS=',' read -ra raw_words <<< "$cpuset"
    local n_words=${#raw_words[@]}
    local wi=0
    for (( i = n_words - 1; i >= 0; i-- )); do
      local word="${raw_words[$i]}"
      if [[ -n "$word" ]]; then
        local val=$(( word ))   # bash handles 0x prefix
        if (( val != 0 )); then
          local bit
          for (( bit = 0; bit < 32; bit++ )); do
            if (( (val >> bit) & 1 )); then
              local cpu_num=$(( wi * 32 + bit ))
              _lcr["$cpu_num"]="$cls"
              found_any=1
            fi
          done
        fi
      fi
      wi=$(( wi + 1 ))
    done
  done

  if [[ $found_any -eq 1 ]]; then return 0; else return 1; fi
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

  # ---- derive isolated CPU set (union of kernel + sysfs sources) --------
  local isolated_range_sysfs=""
  [[ -r /sys/devices/system/cpu/isolated ]] && \
    isolated_range_sysfs="$(tr -d '[:space:]' </sys/devices/system/cpu/isolated 2>/dev/null || true)"
  local isolated_range_cmdline=""
  isolated_range_cmdline="$(get_isolcpus_range_from_proc_cmdline)"

  declare -A _btc_isolated=()
  mark_cpu_set_from_range_list "$isolated_range_sysfs" _btc_isolated
  mark_cpu_set_from_range_list "$isolated_range_cmdline" _btc_isolated

  # ---- derive cgroup-visible CPU set -------------------------------------
  local cgroup_cpuset_range=""
  cgroup_cpuset_range="$(get_current_cgroup_cpuset_range)"
  declare -A _btc_cgroup_allowed=()
  mark_cpu_set_from_range_list "$cgroup_cpuset_range" _btc_cgroup_allowed

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

  # Restrict to CPUs visible to the current cgroup when cpuset is populated.
  local -a filtered_ids=()
  local cpu_id
  if [[ ${#_btc_cgroup_allowed[@]} -gt 0 ]]; then
    for cpu_id in "${sorted_ids[@]}"; do
      [[ -n "${_btc_cgroup_allowed[$cpu_id]:-}" ]] && filtered_ids+=("$cpu_id")
    done
  else
    filtered_ids=("${sorted_ids[@]}")
  fi

  # ---- class classification: lstopo (authoritative) or max-freq (fallback) --
  # lstopo reads CPUID/MIDR registers and emits CPU kinds with efficiency values
  # and optional CoreType attributes (IntelCore/IntelAtom on hybrid Intel).
  # Max-freq is used as a fallback when lstopo is unavailable.
  declare -A _btc_lstopo_class=()
  local _btc_use_lstopo=0
  if classify_cores_via_lstopo _btc_lstopo_class; then
    _btc_use_lstopo=1
    echo "[INFO] CPU class: lstopo classification used ($(command -v lstopo))"
  else
    echo "[INFO] CPU class: lstopo unavailable or single-kind — using max-freq fallback"
  fi

  # Frequency data — always collected as fallback / cross-check
  declare -A _btc_freq_count=() _btc_freq_map=()
  for cpu_id in "${filtered_ids[@]}"; do
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
    if [[ -n "$isolated_range_sysfs" ]]; then
      echo "# isolated source: /sys/devices/system/cpu/isolated=$isolated_range_sysfs"
    fi
    if [[ -n "$isolated_range_cmdline" ]]; then
      echo "# isolated source: /proc/cmdline isolcpus=$isolated_range_cmdline"
    fi
    if [[ -n "$cgroup_cpuset_range" ]]; then
      echo "# cgroup source: cpuset.cpus.effective=$cgroup_cpuset_range"
    else
      echo "# cgroup source: cpuset unavailable/empty (using all discovered physical cores)"
    fi
    printf '# %s\t%s\t%s\t%s\n' "core_id" "arch" "class" "type"
    for cpu_id in "${filtered_ids[@]}"; do
      local ctype="shared"
      [[ -n "${_btc_isolated[$cpu_id]:-}" ]] && ctype="isolated"
      local class
      if [[ $_btc_use_lstopo -eq 1 && -n "${_btc_lstopo_class[$cpu_id]:-}" ]]; then
        class="${_btc_lstopo_class[$cpu_id]}"
      else
        class="$(classify_cpu_frequency_tier "${_btc_freq_map[$cpu_id]}" _btc_sorted_freqs)"
      fi
      printf '%s\t%s\t%s\t%s\n' "$cpu_id" "$arch" "$class" "$ctype"
    done
  } > "$output_file"

  echo "[INFO] CPU topology cached: $output_file (${#filtered_ids[@]} physical cores)"
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

