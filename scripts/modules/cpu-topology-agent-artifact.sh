#!/bin/bash

SCRIPT_DIR_CPU_TOPO_AGENT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./cpu-topology.sh
source "$SCRIPT_DIR_CPU_TOPO_AGENT/cpu-topology.sh"
# shellcheck source=./cache-topology.sh
source "$SCRIPT_DIR_CPU_TOPO_AGENT/cache-topology.sh"

# Read cache topology from TSV and build caches JSON array.
_build_caches_json() {
	local cache_tsv_file="${CACHE_TOPOLOGY_CACHE_FILE:-$HOME/sandbox/cache-topology.tsv}"
	local caches_json='[]'

	if [[ ! -f "$cache_tsv_file" ]]; then
		echo "$caches_json"
		return 0
	fi

	local level id allocation size_kb ways way_size_kb
	while IFS=$'\t' read -r level id allocation size_kb ways way_size_kb; do
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
				'. + [{level: $level, id: $id, size_kb: ($size_kb | tonumber), ways: ($ways | tonumber), way_size_kb: ($way_size_kb | tonumber)}]' <<<"$caches_json"
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

generate_cpu_topology_agent_artifact() {
	local default_output_path="${HOME}/sandbox/poc/device/agent/config/cpu-topology-agent.json"
	local output_path="${1:-$default_output_path}"

	echo "Generating CPU topology artifact for device agent..."
	read_cpu_topology_cache
	if ! export_cpu_topology_agent_json "$output_path"; then
		echo "❌ Failed to generate CPU topology artifact: $output_path"
		return 1
	fi

	echo "✅ CPU topology artifact generated: $output_path"
	echo "[INFO] Agent topology artifact written: $output_path"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	set -euo pipefail
	generate_cpu_topology_agent_artifact "$@"
fi
