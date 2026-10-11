#!/usr/bin/env bash
# Baseline settings applied on every daemon start. Everything here is
# idempotent; "already set" errors from the CLI are expected and ignored.
set -u

log() { echo "[bootstrap] $*"; }
try() { out=$(nordvpn "$@" 2>&1) || log "nordvpn $* -> $(echo "$out" | tr -d '\r' | tail -n1)"; }

# Answer the analytics consent up front, otherwise the first `login` can
# stop at an interactive prompt the panel cannot answer.
try set analytics "${NORD_ANALYTICS:-off}"

# Meshnet only works with NordLynx.
try set technology nordlynx

# Keep Umbrel reachable from the LAN and keep Docker bridge networks
# (10.x / 172.16.x / 192.168.x) outside the tunnel when VPN is connected.
try set lan-discovery "${NORD_LAN_DISCOVERY:-on}"

# Extra subnets to keep outside the tunnel, comma separated,
# e.g. "100.64.0.0/10" when Tailscale also runs on this Umbrel.
IFS=',' read -ra SUBNETS <<< "${NORD_ALLOWLIST_SUBNETS:-}"
for s in "${SUBNETS[@]}"; do
  s="$(echo "$s" | xargs)"
  [ -n "$s" ] && try allowlist add subnet "$s"
done

# Optional token from the app environment. The panel login is the normal path.
if [ -n "${NORDVPN_TOKEN:-}" ] && nordvpn account 2>&1 | grep -qi "not logged in"; then
  log "logging in with NORDVPN_TOKEN"
  try login --token "$NORDVPN_TOKEN"
fi

exit 0
