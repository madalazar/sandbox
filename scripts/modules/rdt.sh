#!/bin/bash
# modules/rdt.sh - Intel Resource Director Technology host setup helpers

if [[ -n "${_RDT_LOADED:-}" ]] && declare -F ensure_intel_rdt_ready >/dev/null; then
  return 0
fi
readonly _RDT_LOADED=1

RDT_CPUINFO_FILE="${RDT_CPUINFO_FILE:-/proc/cpuinfo}"
RDT_KERNEL_CMDLINE_FILE="${RDT_KERNEL_CMDLINE_FILE:-/proc/cmdline}"
RDT_RESCTRL_ROOT="${RDT_RESCTRL_ROOT:-/sys/fs/resctrl}"

# Return 0 if the host looks Intel and advertises RDT-related CPU flags.
_is_intel_rdt_capable_host() {
  local vendor flags
  vendor="$(awk -F: '/^vendor_id/{gsub(/^ +/, "", $2); print $2; exit}' "$RDT_CPUINFO_FILE" 2>/dev/null || true)"
  [[ "$vendor" == "GenuineIntel" ]] || return 1

  flags="$(awk -F: '/^flags/{gsub(/^ +/, "", $2); print $2; exit}' "$RDT_CPUINFO_FILE" 2>/dev/null || true)"
  [[ "$flags" =~ (^|[[:space:]])(cat_l3|cat_l2|cqm|mba)([[:space:]]|$) ]]
}

# Return 0 if the kernel command line enables Intel RDT features.
_is_intel_rdt_enabled_at_boot() {
  local cmdline
  cmdline="$(cat "$RDT_KERNEL_CMDLINE_FILE" 2>/dev/null || true)"

  [[ "$cmdline" == *"intel_rdt=on"* ]] || return 1
  [[ "$cmdline" == *"rdt="* ]]
}

# Return 0 if the kernel command line and resctrl show usable CAT support.
_is_intel_rdt_enabled_and_usable() {
  _is_intel_rdt_enabled_at_boot || return 1
  [[ -d "$RDT_RESCTRL_ROOT" ]] || return 1
  [[ -d "$RDT_RESCTRL_ROOT/info/L3" || -d "$RDT_RESCTRL_ROOT/info/L2" ]]
}

_ensure_resctrl_mounted() {
  if [[ -d "$RDT_RESCTRL_ROOT/info" ]]; then
    return 0
  fi

  local -a privilege_command=()
  if (( EUID != 0 )); then
    command -v sudo >/dev/null 2>&1 || return 1
    privilege_command=(sudo -n)
  fi

  "${privilege_command[@]}" mkdir -p "$RDT_RESCTRL_ROOT" || return 1
  "${privilege_command[@]}" mount -t resctrl resctrl "$RDT_RESCTRL_ROOT" || return 1

  [[ -d "$RDT_RESCTRL_ROOT/info" ]]
}

_print_intel_rdt_enable_instructions() {
  local rdt_params="intel_rdt=on rdt=cmt,mbmtotal,mbmlocal,l3cat,l3cdp,l2cat,l2cdp,mba"

  echo "[ACTION REQUIRED] Intel RDT capabilities detected, but resctrl/CAT is not fully enabled."
  echo "[ACTION REQUIRED] Enable Intel RDT and mount resctrl, then rerun this script."
  echo ""
  echo "Suggested commands (based on Intel CAT wiki guidance):"
  echo "To make it persistent via GRUB (Ubuntu/Debian style):"
  echo "  sudo cp /etc/default/grub /etc/default/grub.bak.$(date +%Y%m%d%H%M%S)"
  echo "  sudo sed -i '/^GRUB_CMDLINE_LINUX=/ s|\"$| ${rdt_params}\"|' /etc/default/grub"
  echo "  cat /etc/default/grub"
  echo "  sudo update-grub"
  echo "  sudo reboot"
}

# Ensure Intel RDT needs no setup or is enabled and usable on the current host.
# Returns 0 when no action is needed and 2 when host setup or intervention is required.
ensure_intel_rdt_ready() {
  if ! _is_intel_rdt_capable_host || _is_intel_rdt_enabled_and_usable; then
    return 0
  fi

  if _is_intel_rdt_enabled_at_boot \
    && _ensure_resctrl_mounted \
    && _is_intel_rdt_enabled_and_usable; then
    return 0
  fi

  _print_intel_rdt_enable_instructions
  return 2
}
