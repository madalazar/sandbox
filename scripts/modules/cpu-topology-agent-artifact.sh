#!/bin/bash

SCRIPT_DIR_CPU_TOPO_AGENT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./cpu-topology.sh
source "$SCRIPT_DIR_CPU_TOPO_AGENT/cpu-topology.sh"
# shellcheck source=./cache-topology.sh
source "$SCRIPT_DIR_CPU_TOPO_AGENT/cache-topology.sh"

_install_pqos_from_source() {
	if ! command -v git >/dev/null 2>&1; then
		echo "[ERROR] git is required to install pqos from source" >&2
		return 1
	fi
	if ! command -v make >/dev/null 2>&1; then
		echo "[ERROR] make is required to install pqos from source" >&2
		return 1
	fi

	local tmp_dir repo_dir
	tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/intel-cmt-cat-XXXXXX")" || return 1
	repo_dir="$tmp_dir/intel-cmt-cat"

	echo "[INFO] Installing pqos from Intel upstream source (intel-cmt-cat/pqos)"
	if ! git clone --depth 1 https://github.com/intel/intel-cmt-cat.git "$repo_dir"; then
		rm -rf "$tmp_dir"
		return 1
	fi

	if ! (
		cd "$repo_dir" &&
		make -C lib &&
		make -C pqos &&
		sudo make -C lib install &&
		sudo make -C pqos install
	); then
		rm -rf "$tmp_dir"
		return 1
	fi

	rm -rf "$tmp_dir"
	return 0
}

ensure_pqos_available() {
	if command -v pqos >/dev/null 2>&1; then
		return 0
	fi

	echo "[WARN] pqos is not installed; attempting source installation"
	if ! _install_pqos_from_source; then
		echo "[ERROR] Failed to install pqos from source" >&2
		return 1
	fi

	if ! command -v pqos >/dev/null 2>&1; then
		echo "[ERROR] pqos is still unavailable after source installation attempt" >&2
		return 1
	fi

	return 0
}

detect_pqos_interface() {
	local forced_iface

	if sudo printenv RDT_IFACE >/dev/null 2>&1; then
		forced_iface="$(sudo printenv RDT_IFACE 2>/dev/null | tr '[:upper:]' '[:lower:]')"
		forced_iface="$(echo "$forced_iface" | tr -d '[:space:]')"
		case "$forced_iface" in
		os|msr)
			echo "$forced_iface"
			return 0
			;;
		esac
		echo "[WARN] Ignoring unsupported RDT_IFACE value: $forced_iface" >&2
	fi

	if sudo pqos --iface=os -s >/dev/null 2>&1; then
		echo "os"
		return 0
	fi

	if sudo pqos --iface=msr -s >/dev/null 2>&1; then
		echo "msr"
		return 0
	fi

	return 1
}

# Read cache topology from TSV and build caches JSON array.
_build_caches_json() {
	local cache_tsv_file="${CACHE_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cache-topology.tsv}"
	local caches_json='[]'

	if [[ ! -f "$cache_tsv_file" ]]; then
		echo "$caches_json"
		return 0
	fi

	local level id allocation_types size_kb ways way_size_kb cores
	while IFS=$'\t' read -r level id allocation_types size_kb ways way_size_kb cores; do
		# Skip comment lines and incomplete entries
		[[ "$level" == "#"* ]] && continue
		[[ -z "$level" || -z "$id" || -z "$size_kb" ]] && continue

		# Strip 'L#' prefix from id and 'KB' suffix from size
		id="${id#L#}"
		size_kb="${size_kb%KB}"

		# Validate numeric fields
		[[ "$id" =~ ^[0-9]+$ ]] || continue
		[[ "$size_kb" =~ ^[0-9]+$ ]] || continue
		[[ "$ways" =~ ^[0-9]+$ ]] || continue
		[[ "$way_size_kb" =~ ^[0-9]+$ ]] || continue

		caches_json="$({
			jq -c \
				--arg level "$level" \
				--arg id "$id" \
				--arg size_kb "$size_kb" \
				--arg ways "$ways" \
				--arg way_size_kb "$way_size_kb" \
				--arg cores "$cores" \
				'. + [{level: $level, id: $id, size_kb: ($size_kb | tonumber), ways: ($ways | tonumber), way_size_kb: ($way_size_kb | tonumber), cores: $cores}]' <<<"$caches_json"
		} )" || return 1
	done < "$cache_tsv_file"

	echo "$caches_json"
}

# Export a deterministic JSON artifact for agent startup.
# This wrapper owns JSON generation; cpu-topology.sh remains TSV-focused.
export_cpu_topology_agent_json() {
	local out_file="$1"
	[[ -z "$out_file" ]] && return 1
	if ! command -v jq >/dev/null 2>&1; then
		echo "[ERROR] jq is required to generate CPU topology artifact JSON" >&2
		return 1
	fi

	if [[ ${#_TOPO_SORTED_IDS[@]} -eq 0 || ${#_TOPO_CORE_META[@]} -eq 0 ]]; then
		read_cpu_topology_cache
	fi

	mkdir -p "$(dirname "$out_file")"

	local cores_json
	cores_json='[]'
	local id
	for id in "${_TOPO_SORTED_IDS[@]}"; do
		IFS='|' read -r _arch _class _type <<<"${_TOPO_CORE_META[$id]}"
		if ! is_isolated_core_type "${_type}"; then
			continue
		fi
		cores_json="$({
			jq -c \
				--argjson id "$id" \
				--arg class "${_class}" \
				--arg type "${_type}" \
				'. + [{id: $id, class: $class, type: $type}]' <<<"$cores_json"
		} )" || return 1
	done

	local ts
	ts="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
	local base_json='{}'
	local existing_cores='' existing_caches=''
	
	local caches_json
	caches_json="$(_build_caches_json)" || return 1
	
	if [[ -s "$out_file" ]] && jq empty "$out_file" >/dev/null 2>&1; then
		base_json="$(cat "$out_file")"
		existing_cores="$(jq -c '.cores // []' <<<"$base_json")"
		existing_caches="$(jq -c '.caches // []' <<<"$base_json")"
		if [[ "$existing_cores" == "$cores_json" && "$existing_caches" == "$caches_json" ]]; then
			echo "[INFO] CPU topology cores and caches unchanged; skipping artifact update: $out_file"
			return 0
		fi
	fi

	jq \
		--arg ts "$ts" \
		--argjson cores "$cores_json" \
		--argjson caches "$caches_json" \
		'.schemaVersion //= "v1" | .generatedAt = $ts | .cores = $cores | .caches = $caches' \
		<<<"$base_json" > "$out_file"
}

update_pqos_interface_in_cpu_topology_agent_artifact() {
	local default_output_path="${HOME}/sandbox/poc/device/agent/config/cpu-topology-agent.json"
	local output_path="${1:-$default_output_path}"

	if ! command -v jq >/dev/null 2>&1; then
		echo "[ERROR] jq is required to update pqos_interface in topology artifact" >&2
		return 1
	fi

	if [[ ! -f "$output_path" ]]; then
		echo "[ERROR] Topology artifact does not exist: $output_path" >&2
		return 1
	fi

	if ! ensure_pqos_available; then
		echo "[ERROR] Failed to install or detect pqos utility" >&2
		return 1
	fi

	local pqos_interface
	if ! pqos_interface="$(detect_pqos_interface)"; then
		echo "[ERROR] No usable pqos interface detected (os or msr)" >&2
		return 1
	fi

	local current_interface
	current_interface="$(jq -r '.pqos_interface // ""' "$output_path")"
	if [[ "$current_interface" == "$pqos_interface" ]]; then
		echo "[INFO] pqos_interface unchanged in topology artifact: $pqos_interface"
		return 0
	fi

	local tmp_file
	tmp_file="$(mktemp)" || return 1
	if ! jq --arg pqos_interface "$pqos_interface" '.schemaVersion //= "v1" | .pqos_interface = $pqos_interface' "$output_path" > "$tmp_file"; then
		rm -f "$tmp_file"
		return 1
	fi

	mv "$tmp_file" "$output_path"
	echo "[INFO] Updated pqos_interface in topology artifact: $pqos_interface"
}

generate_cpu_topology_agent_artifact() {
	local default_output_path="${HOME}/sandbox/poc/device/agent/config/cpu-topology-agent.json"
	local output_path="${1:-$default_output_path}"
	local cache_tsv_file="${CACHE_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cache-topology.tsv}"
	local generated_tmp_cache=""

	echo "Generating CPU topology artifact for device agent..."
	if [[ -e "$cache_tsv_file" && ! -w "$cache_tsv_file" ]]; then
		generated_tmp_cache="$(mktemp "${TMPDIR:-/tmp}/cache-topology-XXXXXX.tsv")" || {
			echo "❌ Failed to allocate writable cache topology file"
			return 1
		}
		cache_tsv_file="$generated_tmp_cache"
		echo "[WARN] Default cache topology file is not writable; using temporary file: $cache_tsv_file"
	fi

	CACHE_TOPOLOGY_CACHE_FILE="$cache_tsv_file"

	if ! build_cache_topology_cache "$cache_tsv_file"; then
		echo "❌ Failed to refresh cache topology cache: $cache_tsv_file"
		if [[ -n "$generated_tmp_cache" ]]; then
			rm -f "$generated_tmp_cache"
		fi
		return 1
	fi
	read_cpu_topology_cache
	if ! export_cpu_topology_agent_json "$output_path"; then
		echo "❌ Failed to generate CPU topology artifact: $output_path"
		if [[ -n "$generated_tmp_cache" ]]; then
			rm -f "$generated_tmp_cache"
		fi
		return 1
	fi

	if [[ -n "$generated_tmp_cache" ]]; then
		rm -f "$generated_tmp_cache"
	fi

	echo "✅ CPU topology artifact generated: $output_path"
	echo "[INFO] Agent topology artifact written: $output_path"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	set -euo pipefail
	generate_cpu_topology_agent_artifact "$@"
fi
