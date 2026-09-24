#!/bin/bash

SCRIPT_DIR_HOST_TOPOLOGY="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=./cpu-topology.sh
source "$SCRIPT_DIR_HOST_TOPOLOGY/cpu-topology.sh"
# shellcheck source=./cache-topology.sh
source "$SCRIPT_DIR_HOST_TOPOLOGY/cache-topology.sh"

HOST_TOPOLOGY_FILE="${HOST_TOPOLOGY_FILE:-$HOME/sandbox/poc/device/agent/config/host-topology.json}"

# Print isolated CPU topology as a JSON array; write errors to stderr.
_build_cpu_topology_json() {
	local cpu_tsv_file="${1:-}"
	if [[ -z "$cpu_tsv_file" ]]; then
		echo "[ERROR] CPU topology input path is required" >&2
		return 1
	fi

	local topology_json
	topology_json="$(read_cpu_topology_as_json "$cpu_tsv_file")" || {
		echo "[ERROR] Failed to read CPU topology: $cpu_tsv_file" >&2
		return 1
	}

	yq eval -o=json -I=0 '[.[] | select(.type == "isolated") | {"id": .id, "class": .class, "type": .type}]' - <<<"$topology_json"
}

# Print cache topology as a JSON array; write errors to stderr.
_build_cache_topology_json() {
	local cache_tsv_file="${1:-}"
	if [[ -z "$cache_tsv_file" ]]; then
		echo "[ERROR] Cache topology input path is required" >&2
		return 1
	fi

	local topology_json
	topology_json="$(read_cache_topology_as_json "$cache_tsv_file")" || {
		echo "[ERROR] Failed to read cache topology: $cache_tsv_file" >&2
		return 1
	}

	yq eval -o=json -I=0 '[.[] | {
		"level": .level,
		"id": (.id | sub("^L#", "")),
		"size_kb": .sizeKiB,
		"ways": .ways,
		"way_size_kb": .waySizeKiB,
		"cores": .cores
	}]' - <<<"$topology_json"
}

# Atomically write the combined topology arrays to the host artifact.
_write_host_topology_json() {
	local output_file="${1:-}"
	local cores_json="${2:-}"
	local caches_json="${3:-}"
	local max_clos="${4:-}"
	if [[ -z "$output_file" || -z "$cores_json" || -z "$caches_json" || -z "$max_clos" ]]; then
		echo "[ERROR] Host topology output path, JSON arrays and max_clos are required" >&2
		return 1
	fi

	local output_dir
	output_dir="$(dirname "$output_file")"
	if ! mkdir -p "$output_dir"; then
		echo "[ERROR] Failed to create host topology output directory: $output_dir" >&2
		return 1
	fi

	local base_json='{}'
	local existing_cores existing_caches existing_clos
	if [[ -s "$output_file" ]] && yq eval -e '.' - < "$output_file" >/dev/null 2>&1; then
		base_json="$(<"$output_file")"
		# generatedAt and other existing fields remain unchanged when topology matches.
		existing_cores="$(yq eval -o=json -I=0 '.cores // []' - <<<"$base_json")" || return 1
		existing_caches="$(yq eval -o=json -I=0 '.caches // []' - <<<"$base_json")" || return 1
		existing_clos="$(yq eval -r '.max_clos // 0' - <<<"$base_json")" || return 1
		if [[ "$existing_cores" == "$cores_json" && "$existing_caches" == "$caches_json" && "$existing_clos" == "$max_clos" ]]; then
			echo "[INFO] Host topology unchanged; skipping artifact update: $output_file"
			return 0
		fi
	elif [[ -s "$output_file" ]]; then
		echo "[WARN] Replacing invalid host topology JSON: $output_file" >&2
	fi

	local generated_at tmp_file
	generated_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')" || return 1
	if ! tmp_file="$(mktemp "$output_dir/.host-topology.XXXXXX")"; then
		echo "[ERROR] Failed to create temporary host topology file in: $output_dir" >&2
		return 1
	fi

	if ! GENERATED_AT="$generated_at" \
		CORES_JSON="$cores_json" \
		CACHES_JSON="$caches_json" \
		MAX_CLOS="$max_clos" \
		yq eval -o=json -P '
			.schemaVersion = (.schemaVersion // "v1") |
			.generatedAt = env(GENERATED_AT) |
			.cores = env(CORES_JSON) |
			.caches = env(CACHES_JSON) |
			.max_clos = env(MAX_CLOS)
		' - <<<"$base_json" > "$tmp_file"; then
		rm -f "$tmp_file"
		echo "[ERROR] Failed to serialize host topology JSON" >&2
		return 1
	fi

	if [[ -e "$output_file" ]] && ! chmod --reference="$output_file" "$tmp_file"; then
		rm -f "$tmp_file"
		echo "[ERROR] Failed to preserve permissions for host topology: $output_file" >&2
		return 1
	fi
	if ! mv -f "$tmp_file" "$output_file"; then
		rm -f "$tmp_file"
		echo "[ERROR] Failed to persist host topology: $output_file" >&2
		return 1
	fi
}

# Export a deterministic JSON artifact from existing CPU and cache topology TSV files.
generate_topology_artefact() {
	local output_path="${1:-$HOST_TOPOLOGY_FILE}"
	local cpu_tsv_file="${2:-$CPU_TOPOLOGY_CACHE_FILE}"
	local cache_tsv_file="${3:-$CACHE_TOPOLOGY_CACHE_FILE}"
	local max_clos
	max_clos="$(get_device_max_closids)"

	if ! command -v yq >/dev/null 2>&1; then
		echo "[ERROR] yq is required to generate host topology JSON" >&2
		return 1
	fi

	local cores_json caches_json
	if ! cores_json="$(_build_cpu_topology_json "$cpu_tsv_file")" ||
		! caches_json="$(_build_cache_topology_json "$cache_tsv_file")" ||
		! _write_host_topology_json "$output_path" "$cores_json" "$caches_json" "$max_clos"; then
		echo "[ERROR] Failed to generate host topology artifact: $output_path" >&2
		return 1
	fi

	echo "[INFO] Host topology artifact written: $output_path"
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
	set -euo pipefail
	generate_topology_artefact "$@"
fi