#!/bin/bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./cpu-topology.sh
source "$SCRIPT_DIR/cpu-topology.sh"

# Export a deterministic JSON artifact for agent startup.
# This wrapper owns JSON generation; cpu-topology.sh remains TSV-focused.
export_cpu_topology_agent_json() {
	local out_file="$1"
	[[ -z "$out_file" ]] && return 1

	if [[ ${#_TOPO_SORTED_IDS[@]} -eq 0 || ${#_TOPO_CORE_META[@]} -eq 0 ]]; then
		read_cpu_topology_cache
	fi

	mkdir -p "$(dirname "$out_file")"

	{
		printf '{\n'
		printf '  "schemaVersion": "v1",\n'
		printf '  "generatedAt": "%s",\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
		printf '  "cores": [\n'

		local i id total
		total=${#_TOPO_SORTED_IDS[@]}
		for ((i=0; i<total; i++)); do
			id="${_TOPO_SORTED_IDS[$i]}"
			IFS='|' read -r _arch _class _type <<<"${_TOPO_CORE_META[$id]}"
			printf '    { "id": %s, "class": "%s", "type": "%s" }' "$id" "${_class}" "${_type}"
			if (( i < total - 1 )); then
				printf ',\n'
			else
				printf '\n'
			fi
		done

		printf '  ]\n'
		printf '}\n'
	} > "$out_file"
}

OUTPUT_PATH="${1:-${HOME}/sandbox/cpu-topology-agent.json}"

read_cpu_topology_cache
export_cpu_topology_agent_json "$OUTPUT_PATH"

echo "[INFO] Agent topology artifact written: $OUTPUT_PATH"
