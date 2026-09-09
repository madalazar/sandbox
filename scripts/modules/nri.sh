#!/bin/bash
# modules/nri.sh - NRI Balloon Resource Policy plugin management
#
# Functions:
#   generate_default_nri_policy - Generate default NRI policy with per-core balloons from host topology
#   install_balloon_nri_plugin  - Helm-install the NRI balloon plugin
#   update_balloon_nri_plugin   - Helm-upgrade the NRI balloon plugin
#   uninstall_balloon_nri_plugin - Helm-uninstall the NRI balloon plugin
#
# Prerequisites:
#   - k3s environment with a running device agent
#   - capabilities.sh helpers must be sourced (or this file self-sources them)

SCRIPT_DIR_NRI="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
source "${SCRIPT_DIR_NRI}/cpu-topology.sh"
source "${SCRIPT_DIR_NRI}/cache-topology.sh"

# ---------------------------------------------------------------------------
# Helpers
# ---------------------------------------------------------------------------

_nri_extract_available_cpus_from_policy() {
  local policy_file="$1"
  [[ -r "$policy_file" ]] || return 1

  local cpu_val
  cpu_val="$(yq -r '.config.availableResources.cpu // ""' "$policy_file" 2>/dev/null || true)"
  [[ -n "$cpu_val" && "$cpu_val" != "null" ]] || return 1

  echo "${cpu_val#cpuset:}"
}

_nri_build_rdt_control_yaml() {
  # Generates a minimal RDT configuration with fullCache: null
  # to override default chart values and allow dynamic partition creation.
  yq -n '
    .control.rdt.enable = true |
    .control.rdt.usePodQoSAsDefaultClass = false |
    .control.rdt.options.l3.optional = true |
    .control.rdt.partitions.fullCache = null
  '
}

# ---------------------------------------------------------------------------
# generate_default_nri_policy
#
# Generates a default NRI Balloon Resource Policy with:
#   - Available CPUs derived from host CPU topology (or custom range)
#   - Reserved CPUs defaulting to core 0, + 1 core if > 30 cores, + 2 cores if > 60 cores
#   - Individual balloons for each isolated CPU core (ipc<class><core_id>)
#   - Shared cores delegated to NRI's default balloon (no custom balloon)
#   - RDT control block with fullCache: null
#
# Naming convention for isolated balloons:
#   i<class_abbrev>c<core_id>
#   - i: isolated type prefix
#   - <class_abbrev>: first letter of class (p=performance, e=efficiency, l=low-power)
#   - c: separator constant
#   - <core_id>: CPU index (0-N)
#   Examples: ipc0, ipc1, ipe5, ilc2
#
# Usage: generate_default_nri_policy [available_cpus [reserved_cpus [output_file [topology_cache_file]]]]
#   available_cpus:      cpuset range string (default: derived from topology cache)
#   reserved_cpus:       cpuset range string (default: core 0, + last core if > 30 cores, + last 2 cores if > 60 cores)
#   output_file:         destination for YAML (default: $HOME/sandbox/balloon-policy.yaml)
#   topology_cache_file: topology cache file (default: $CPU_TOPOLOGY_CACHE_FILE)
# ---------------------------------------------------------------------------
generate_default_nri_policy() {
  local available_cpus_range="${1:-}"
  local reserved_cpus_range="${2:-}"
  local output_file="${3:-$HOME/sandbox/balloon-policy.yaml}"
  local topology_cache_file="${4:-$CPU_TOPOLOGY_CACHE_FILE}"

  local cpu_topology_json
  if ! cpu_topology_json="$(read_cpu_topology_as_json "$topology_cache_file")"; then
    echo "[ERROR] Failed to read CPU topology from: $topology_cache_file" >&2
    return 1
  fi

  local cpu_count
  cpu_count="$(jq 'length' <<< "$cpu_topology_json" 2>/dev/null || echo 0)"
  if [[ "$cpu_count" -eq 0 ]]; then
    echo "[ERROR] No CPUs discovered from host topology cache." >&2
    return 1
  fi

  # Derive available_cpus_range from CPU topology if not explicitly provided
  if [[ -z "$available_cpus_range" ]]; then
    available_cpus_range="$(jq -r '
      [.[].id | tonumber] | sort as $ids |
      reduce $ids[] as $x ([];
        if length == 0 then [[$x, $x]]
        elif .[-1][1] + 1 == $x then .[-1][1] = $x
        else . + [[$x, $x]]
        end
      ) | map(if .[0] == .[1] then "\(.[0])" else "\(.[0])-\(.[1])" end) | join(",")
    ' <<< "$cpu_topology_json")"
  fi

  # Derive reserved_cpus_range if not explicitly provided:
  # Core 0 is always included; add 1 core (last one) if > 30 cores, or last 2 cores if > 60 cores.
  if [[ -z "$reserved_cpus_range" ]]; then
    reserved_cpus_range="$(jq -r '
      [.[].id | tonumber] | sort as $ids |
      ($ids | length) as $cnt |
      (if $cnt > 60 then ([$ids[0]] + $ids[-2:])
       elif $cnt > 30 then ([$ids[0]] + [$ids[-1]])
       else [$ids[0]]
       end) | unique | sort | join(",")
    ' <<< "$cpu_topology_json")"
  fi

  # Generate balloon definitions for isolated CPUs
  local balloon_types_json
  balloon_types_json="$(jq -c '
    [ .[] | select(.type == "isolated") |
      (if .class == "performance" then "p"
       elif .class == "efficiency" then "e"
       elif .class == "low-power" then "l"
       else "?" end) as $abbrev |
      {
        name: "i\($abbrev)c\(.id)",
        allocatorPriority: "high",
        minBalloons: 1,
        maxBalloons: 1,
        minCPUs: 1,
        maxCPUs: 1,
        preferIsolCpus: true,
        preferCloseToDevices: [
          "/sys/devices/system/cpu/cpu\(.id)/cache/index2"
        ]
      }
    ]
  ' <<< "$cpu_topology_json")"

  local isolated_count
  isolated_count="$(jq 'length' <<< "$balloon_types_json" 2>/dev/null || echo 0)"
  if [[ "$isolated_count" -eq 0 ]]; then
    echo "[WARN] No isolated CPUs found in topology. Generating policy with only shared cores." >&2
  fi

  # Build full configuration JSON and emit formatted YAML via yq
  local full_json
  full_json="$(jq -n \
    --arg available "cpuset:${available_cpus_range}" \
    --arg reserved "cpuset:${reserved_cpus_range}" \
    --argjson balloons "$balloon_types_json" \
    '{
      nri: {
        runtime: { patchConfig: false },
        plugin: { index: 10 }
      },
      config: {
        pinCPU: true,
        pinMemory: false,
        availableResources: { cpu: $available },
        reservedResources: { cpu: $reserved },
        balloonTypes: (if ($balloons | length) > 0 then $balloons else [] end),
        control: {
          rdt: {
            enable: true,
            usePodQoSAsDefaultClass: false,
            options: {
              l3: { optional: true }
            },
            partitions: {
              fullCache: null
            }
          }
        }
      }
    }'
  )"

  mkdir -p "$(dirname "$output_file")"
  {
    cat <<'HEADER'
# Default NRI Balloon Resource Policy
# Generated with per-core isolated balloons from host topology
#
# This policy defines:
#   - availableResources: fixed CPU range (configured at generation time)
#   - reservedResources: fixed reserved CPU range
#   - Per-core balloons for each isolated CPU (ipc<class><core_id>)
#   - Shared cores: delegated to NRI's default balloon (no custom definition needed)
#
# Install:
#   helm repo add nri-plugins https://containers.github.io/nri-plugins
#   helm repo update
#   helm install nri-resource-policy-balloons \
#     nri-plugins/nri-resource-policy-balloons \
#     --namespace kube-system -f balloon-policy.yaml
HEADER
    echo "$full_json" | yq eval -P -
  } > "$output_file"

  echo "✅ Default balloon policy written to: $output_file"
  echo "[INFO] Available CPUs: cpuset:${available_cpus_range}"
  echo "[INFO] Reserved CPUs: cpuset:${reserved_cpus_range}"
  echo "[INFO] Isolated balloons generated: ${isolated_count}"
  echo "[INFO] Shared cores will use NRI default balloon"
  echo "[INFO] RDT control configured"
}


# ---------------------------------------------------------------------------
# install_balloon_nri_plugin [values_file]
#   If values_file is omitted or the path does not yet exist the default
#   balloon policy is generated automatically from host CPU topology.
# ---------------------------------------------------------------------------
install_balloon_nri_plugin() {
  local values_file="${1:-$HOME/sandbox/balloon-policy.yaml}"

  if [[ ! -f "$values_file" ]]; then
    echo "[INFO] No policy file found at '$values_file' — generating default policy from host topology..."
    if ! generate_default_nri_policy "" "" "$values_file"; then
      echo "[ERROR] Failed to generate default NRI balloon policy."
      echo "        Usage: install_balloon_nri_plugin [values_file]"
      return 1
    fi
    echo ""
  fi

  echo "Installing NRI Balloon Resource Policy plugin..."

  if helm repo list 2>/dev/null | grep -q "^nri-plugins[[:space:]]"; then
    echo "✅ nri-plugins helm repo already added, skipping."
  else
    echo "Adding nri-plugins helm repo..."
    helm repo add nri-plugins https://containers.github.io/nri-plugins
  fi

  helm repo update

  helm install nri-resource-policy-balloons nri-plugins/nri-resource-policy-balloons \
    --namespace kube-system \
    -f "$values_file"

  echo "✅ NRI Balloon Resource Policy plugin installed."
}

# ---------------------------------------------------------------------------
# update_balloon_nri_plugin <values_file>
# ---------------------------------------------------------------------------
update_balloon_nri_plugin() {
  local values_file="${1}"
  if [[ -z "$values_file" ]]; then
    echo "[ERROR] Values file is required."
    echo "Usage: update_balloon_nri_plugin <balloons-values-file>"
    return 1
  fi
  if [[ ! -f "$values_file" ]]; then
    echo "[ERROR] Values file not found: $values_file"
    return 1
  fi

  echo "Upgrading NRI Balloon Resource Policy plugin..."

  helm upgrade nri-resource-policy-balloons nri-plugins/nri-resource-policy-balloons \
    --namespace kube-system \
    -f "$values_file"

  echo "✅ NRI Balloon Resource Policy plugin upgraded."
}

# ---------------------------------------------------------------------------
# uninstall_balloon_nri_plugin
# ---------------------------------------------------------------------------
uninstall_balloon_nri_plugin() {
  echo "Uninstalling NRI Balloon Resource Policy plugin..."
  helm uninstall nri-resource-policy-balloons -n kube-system
  echo "✅ NRI Balloon Resource Policy plugin uninstalled."
}
