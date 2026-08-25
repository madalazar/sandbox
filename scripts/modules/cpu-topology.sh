#!/bin/bash
# modules/cpu-topology.sh - Shared CPU topology helpers
#
# Provides three layers:
#   1. Pure helpers  — arch mapping, cpuset parsing, freq classification, lstopo classification
#   2. Cache builder — build_cpu_topology: full sysfs inspection → TSV file
#   3. JSON reader   — read_cpu_topology_as_json: persisted TSV → JSON array
#
# Cache file format (tab-separated, one physical core per line):
#   # comments / header lines starting with #
#   <core_id>  <arch>  <class>  <type>
#
# Sourcing is idempotent (load-guard at the top).

[[ -n "${_CPU_TOPOLOGY_LOADED:-}" ]] && return 0
readonly _CPU_TOPOLOGY_LOADED=1

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
    *)
      echo "[ERROR] unsupported machine architecture: $machine_arch" >&2
      return 1
      ;;
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
  local -a part_list=()

  [[ -z "$range_list" ]] && return 0

  IFS=',' read -ra part_list <<<"$range_list"
  for part in "${part_list[@]}"; do
    if [[ "$part" =~ ^([0-9]+)-([0-9]+)$ ]]; then
      local start="${BASH_REMATCH[1]}" end="${BASH_REMATCH[2]}" cpu
      for ((cpu=start; cpu<=end; cpu++)); do
        cpu_set_ref["$cpu"]=1
      done
    elif [[ "$part" =~ ^[0-9]+$ ]]; then
      # shellcheck disable=SC2034  # Assignment is through a nameref.
      cpu_set_ref["$part"]=1
    fi
  done
}

# Print one CPU from a sibling range, preferring the lowest CPU visible in the
# supplied allowed set. Returns 1 when the set is populated but no sibling is
# allowed.
select_cpu_from_sibling_range() {
  local sibling_range="$1"
  local fallback_cpu="$2"
  local -n allowed_set_ref="$3"
  declare -A sibling_set=()
  mark_cpu_set_from_range_list "$sibling_range" sibling_set
  [[ ${#sibling_set[@]} -eq 0 ]] && sibling_set["$fallback_cpu"]=1

  local cpu_id
  while IFS= read -r cpu_id; do
    if [[ ${#allowed_set_ref[@]} -eq 0 || -n "${allowed_set_ref[$cpu_id]:-}" ]]; then
      echo "$cpu_id"
      return 0
    fi
  done < <(printf '%s\n' "${!sibling_set[@]}" | sort -n)

  return 1
}

# Return a stable identity for all logical CPUs in one sibling group.
get_cpu_sibling_group_id() {
  local sibling_range="$1"
  local fallback_cpu="$2"
  local first_sibling="${sibling_range%%,*}"

  if [[ "$first_sibling" =~ ^([0-9]+)-[0-9]+$ ]]; then
    echo "${BASH_REMATCH[1]}"
  elif [[ "$first_sibling" =~ ^[0-9]+$ ]]; then
    echo "$first_sibling"
  else
    echo "$fallback_cpu"
  fi
}

cpu_range_intersects_set() {
  local cpu_range="$1"
  local fallback_cpu="$2"
  local -n target_set_ref="$3"
  declare -A cpu_set=()
  mark_cpu_set_from_range_list "$cpu_range" cpu_set
  [[ ${#cpu_set[@]} -eq 0 ]] && cpu_set["$fallback_cpu"]=1

  local cpu_id
  for cpu_id in "${!cpu_set[@]}"; do
    [[ -n "${target_set_ref[$cpu_id]:-}" ]] && return 0
  done
  return 1
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
  local base="/sys/fs/cgroup"
  local v1_rel=""
  local line
  while IFS= read -r line; do
    # cgroup v2 format: 0::/path
    if [[ "$line" =~ ^0::(.*)$ ]]; then
      rel="${BASH_REMATCH[1]}"
      [[ -z "$rel" ]] && rel="/"
      break
    fi

    # cgroup v1 format: hierarchy:controllers:/path
    local controllers candidate_rel
    IFS=':' read -r _ controllers candidate_rel <<< "$line"
    if [[ ",$controllers," == *,cpuset,* ]]; then
      v1_rel="${candidate_rel:-/}"
    fi
  done < /proc/self/cgroup

  if [[ -n "$v1_rel" && "$line" != 0::* ]]; then
    rel="$v1_rel"
    base="/sys/fs/cgroup/cpuset"
    [[ -d "$base" ]] || base="/sys/fs/cgroup"
  fi

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

  if [[ "$freq" == "$highest" ]]; then
    echo "performance"
  elif [[ "$freq" == "$lowest" ]]; then
    if ((tiers_count >= 3)); then
      echo "low-power"
    else
      echo "efficiency"
    fi
  else
    echo "efficiency"
  fi
}

# Map one parsed hwloc CPU kind to a Margo CPU class.
classify_lstopo_cpu_kind() {
  local core_type="$1"
  local efficiency="$2"
  local kind_count="$3"
  local max_efficiency="$4"
  local min_efficiency="$5"

  if [[ "$core_type" == "IntelCore" ]]; then
    echo "performance"
  elif [[ "$core_type" == "IntelAtom" ]]; then
    if ((kind_count >= 3 && efficiency == min_efficiency)); then
      echo "low-power"
    else
      echo "efficiency"
    fi
  elif ((efficiency == max_efficiency)); then
    echo "performance"
  elif ((kind_count >= 3 && efficiency == min_efficiency)); then
    echo "low-power"
  else
    echo "efficiency"
  fi
}

# Expand an hwloc cpuset into CPU IDs associated with one class.
# Cpuset words are comma-separated, most-significant first.
expand_lstopo_cpuset() {
  local cpuset="$1"
  local cpu_class="$2"
  local -n result_ref="$3"
  local -a words=()
  IFS=',' read -ra words <<< "$cpuset"

  local found_any=false
  local word_index=0
  local array_index word value bit cpu_id
  for ((array_index = ${#words[@]} - 1; array_index >= 0; array_index--)); do
    word="${words[$array_index]}"
    if [[ -n "$word" ]]; then
      value=$((word))
      for ((bit = 0; bit < 32; bit++)); do
        if (((value >> bit) & 1)); then
          cpu_id=$((word_index * 32 + bit))
          # shellcheck disable=SC2034  # Assignment is through a nameref.
          result_ref["$cpu_id"]="$cpu_class"
          found_any=true
        fi
      done
    fi
    word_index=$((word_index + 1))
  done

  [[ "$found_any" == true ]]
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
  declare -A kind_efficiency=()
  declare -A kind_cpuset=()
  declare -A kind_core_type=()
  local current_kind="" in_kind=0

  while IFS= read -r line; do
    # Match: "CPU kind #N efficiency E cpuset 0x..."
    if [[ "$line" =~ ^CPU[[:space:]]kind[[:space:]]#([0-9]+)[[:space:]]efficiency[[:space:]]([0-9]+)[[:space:]]cpuset[[:space:]]([^[:space:]]+) ]]; then
      current_kind="${BASH_REMATCH[1]}"
      kind_efficiency["$current_kind"]="${BASH_REMATCH[2]}"
      kind_cpuset["$current_kind"]="${BASH_REMATCH[3]}"
      kind_core_type["$current_kind"]=""
      in_kind=1
    # Match: indented "CoreType = "IntelCore"" or "info CoreType = "IntelCore""
    elif [[ $in_kind -eq 1 && "$line" =~ CoreType[[:space:]]*=[[:space:]]*\"([^\"]+)\" ]]; then
      kind_core_type["$current_kind"]="${BASH_REMATCH[1]}"
    # Non-indented line that is not a CPU kind line → exit kind context
    elif [[ $in_kind -eq 1 && -n "$line" && ! "$line" =~ ^[[:space:]] ]]; then
      in_kind=0
      current_kind=""
    fi
  done <<< "$lstopo_out"

  local kind_count=${#kind_efficiency[@]}
  [[ $kind_count -eq 0 ]] && return 1

  # --- Phase 2: determine class per kind ----------------------------------
  local max_efficiency=0 min_efficiency=999999
  local kind_id efficiency
  for kind_id in "${!kind_efficiency[@]}"; do
    efficiency="${kind_efficiency[$kind_id]}"
    if ((efficiency > max_efficiency)); then
      max_efficiency=$efficiency
    fi
    if ((efficiency < min_efficiency)); then
      min_efficiency=$efficiency
    fi
  done

  declare -A kind_class=()
  for kind_id in "${!kind_efficiency[@]}"; do
    kind_class["$kind_id"]="$(classify_lstopo_cpu_kind \
      "${kind_core_type[$kind_id]}" "${kind_efficiency[$kind_id]}" \
      "$kind_count" "$max_efficiency" "$min_efficiency")"
  done

  # --- Phase 3: expand cpusets → cpu_id → class ---------------------------
  local found_any=0
  for kind_id in "${!kind_cpuset[@]}"; do
    expand_lstopo_cpuset \
      "${kind_cpuset[$kind_id]}" "${kind_class[$kind_id]}" _lcr && found_any=1
  done

  [[ $found_any -eq 1 ]]
}

# ---------------------------------------------------------------------------
# 2. Cache builder
# ---------------------------------------------------------------------------

# Inspect the host's physical CPU topology and write a TSV cache file.
# HT siblings are collapsed — only one representative per physical core.
# Usage: build_cpu_topology [output_file]
build_cpu_topology() {
  local output_file="${1:-$CPU_TOPOLOGY_CACHE_FILE}"
  local output_dir
  output_dir="$(dirname "$output_file")"
  if ! mkdir -p "$output_dir"; then
    echo "[ERROR] failed to create CPU topology cache directory: $output_dir" >&2
    return 1
  fi

  local arch
  if ! arch="$(map_machine_arch_to_capability_arch "$(uname -m)")"; then
    return 1
  fi

  # ---- derive isolated CPU set (union of kernel + sysfs sources) --------
  local isolated_range_sysfs=""
  [[ -r /sys/devices/system/cpu/isolated ]] && \
    isolated_range_sysfs="$(tr -d '[:space:]' </sys/devices/system/cpu/isolated 2>/dev/null || true)"
  local isolated_range_cmdline=""
  isolated_range_cmdline="$(get_isolcpus_range_from_proc_cmdline)"

  declare -A isolated_cpus=()
  mark_cpu_set_from_range_list "$isolated_range_sysfs" isolated_cpus
  mark_cpu_set_from_range_list "$isolated_range_cmdline" isolated_cpus

  # ---- derive cgroup-visible CPU set -------------------------------------
  local cgroup_cpuset_range=""
  cgroup_cpuset_range="$(get_current_cgroup_cpuset_range)"
  # shellcheck disable=SC2034  # Array is consumed through a nameref.
  declare -A allowed_cpus=()
  mark_cpu_set_from_range_list "$cgroup_cpuset_range" allowed_cpus

  # ---- enumerate physical cores (collapse HT siblings) -------------------
  declare -A seen_sibling_groups=()
  declare -A isolated_physical_cpus=()
  local -a physical_ids=()
  local discovered_cpu_count=0
  local cpu_path
  for cpu_path in /sys/devices/system/cpu/cpu[0-9]*; do
    [[ -d "$cpu_path" ]] || continue
    local cpu_name="${cpu_path##*/}"
    local cpu_idx="${cpu_name#cpu}"
    [[ "$cpu_idx" =~ ^[0-9]+$ ]] || continue
    discovered_cpu_count=$((discovered_cpu_count + 1))

    local siblings=""
    local siblings_file="$cpu_path/topology/thread_siblings_list"
    [[ -r "$siblings_file" ]] && \
      siblings="$(tr -d '[:space:]' <"$siblings_file" 2>/dev/null || true)"

    local sibling_group_id
    sibling_group_id="$(get_cpu_sibling_group_id "$siblings" "$cpu_idx")"
    [[ -n "${seen_sibling_groups[$sibling_group_id]:-}" ]] && continue
    seen_sibling_groups["$sibling_group_id"]=1

    local selected_cpu
    if ! selected_cpu="$(select_cpu_from_sibling_range "$siblings" "$cpu_idx" allowed_cpus)"; then
      continue
    fi
    physical_ids+=("$selected_cpu")
    if cpu_range_intersects_set "$siblings" "$cpu_idx" isolated_cpus; then
      isolated_physical_cpus["$selected_cpu"]=1
    fi
  done

  if [[ "${#physical_ids[@]}" -eq 0 ]]; then
    if ((discovered_cpu_count > 0)); then
      echo "[ERROR] no physical CPU cores are visible to the current cgroup" >&2
      return 1
    fi

    local total
    total="$(nproc --all 2>/dev/null || echo 1)"
    local fallback_idx
    for ((fallback_idx=0; fallback_idx<total; fallback_idx++)); do
      physical_ids+=("$fallback_idx")
      [[ -n "${isolated_cpus[$fallback_idx]:-}" ]] && isolated_physical_cpus["$fallback_idx"]=1
    done
  fi

  # Sort numerically
  local -a sorted_ids=()
  while IFS= read -r id; do sorted_ids+=("$id"); done \
    < <(printf '%s\n' "${physical_ids[@]}" | sort -n)

  local cpu_id

  # ---- class classification: lstopo (authoritative) or max-freq (fallback) --
  # lstopo reads CPUID/MIDR registers and emits CPU kinds with efficiency values
  # and optional CoreType attributes (IntelCore/IntelAtom on hybrid Intel).
  # Max-freq is used as a fallback when lstopo is unavailable.
  declare -A lstopo_classes=()
  local use_lstopo=0
  if classify_cores_via_lstopo lstopo_classes; then
    use_lstopo=1
    echo "[INFO] CPU class: lstopo classification used ($(command -v lstopo))"
  else
    echo "[INFO] CPU class: lstopo unavailable or single-kind — using max-freq fallback"
  fi

  # Frequency data — always collected as fallback / cross-check
  declare -A frequency_counts=()
  declare -A cpu_frequencies=()
  for cpu_id in "${sorted_ids[@]}"; do
    local freq_file="/sys/devices/system/cpu/cpu${cpu_id}/cpufreq/cpuinfo_max_freq"
    local freq="0"
    if [[ -r "$freq_file" ]]; then
      freq="$(tr -d '[:space:]' <"$freq_file" 2>/dev/null || true)"
      [[ "$freq" =~ ^[0-9]+$ ]] || freq="0"
    fi
    cpu_frequencies["$cpu_id"]="$freq"
    frequency_counts["$freq"]=$(( ${frequency_counts["$freq"]:-0} + 1 ))
  done

  local -a sorted_frequencies=()
  while IFS= read -r f; do
    [[ -n "$f" ]] && sorted_frequencies+=("$f")
  done \
    < <(printf '%s\n' "${!frequency_counts[@]}" | sort -nr)

  # ---- write TSV ---------------------------------------------------------
  local tmp_file
  if ! tmp_file="$(mktemp "$output_dir/.cpu-topology.XXXXXX")"; then
    echo "[ERROR] failed to create temporary CPU topology cache in: $output_dir" >&2
    return 1
  fi

  if ! {
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
    for cpu_id in "${sorted_ids[@]}"; do
      local ctype="shared"
      [[ -n "${isolated_physical_cpus[$cpu_id]:-}" ]] && ctype="isolated"
      local class
      if [[ $use_lstopo -eq 1 && -n "${lstopo_classes[$cpu_id]:-}" ]]; then
        class="${lstopo_classes[$cpu_id]}"
      else
        class="$(classify_cpu_frequency_tier "${cpu_frequencies[$cpu_id]}" sorted_frequencies)"
      fi
      printf '%s\t%s\t%s\t%s\n' "$cpu_id" "$arch" "$class" "$ctype"
    done
  } > "$tmp_file"; then
    rm -f "$tmp_file"
    echo "[ERROR] failed to write CPU topology cache: $output_file" >&2
    return 1
  fi

  if ! mv -f "$tmp_file" "$output_file"; then
    rm -f "$tmp_file"
    echo "[ERROR] failed to persist CPU topology cache: $output_file" >&2
    return 1
  fi

  echo "[INFO] CPU topology cached: $output_file (${#sorted_ids[@]} physical cores)"
}

# ---------------------------------------------------------------------------
# 3. JSON reader
# ---------------------------------------------------------------------------

# Print the persisted CPU topology as a JSON array.
# Usage: read_cpu_topology_as_json [cache_file]
read_cpu_topology_as_json() {
  local cache_file="${1:-$CPU_TOPOLOGY_CACHE_FILE}"

  if ! command -v jq >/dev/null 2>&1; then
    echo "[ERROR] jq is required to serialize CPU topology" >&2
    return 1
  fi
  if [[ ! -f "$cache_file" ]]; then
    echo "[ERROR] CPU topology file does not exist: $cache_file" >&2
    return 1
  fi
  if [[ ! -r "$cache_file" ]]; then
    echo "[ERROR] CPU topology file is unreadable: $cache_file" >&2
    return 1
  fi

  local topology_json='[]'
  local id arch class type
  while IFS=$'\t' read -r id arch class type; do
    [[ "$id" =~ ^[0-9]+$ ]] || continue
    topology_json="$(jq -c \
      --argjson id "$id" \
      --arg architecture "$arch" \
      --arg class "$class" \
      --arg type "$type" \
      '. + [{id: $id, architecture: $architecture, class: $class, type: $type}]' \
      <<< "$topology_json")" || return 1
  done < "$cache_file"

  printf '%s\n' "$topology_json"
}

