#!/usr/bin/env bash
# Modes:
#   daemon  run nordvpnd (needs host network + privileges), apply baseline settings
#   panel   run the web panel against the daemon socket in /run/nordvpn
#   *       exec anything else (e.g. `nordvpn status` for debugging)
set -uo pipefail

SOCK=/run/nordvpn/nordvpnd.sock

log() { echo "[nordvpn-umbrel] $*"; }

wait_for_daemon() {
  local tries=${1:-60}
  for _ in $(seq "$tries"); do
    nordvpn status >/dev/null 2>&1 && return 0
    sleep 1
  done
  return 1
}

run_daemon() {
  mkdir -p /run/nordvpn /var/lib/nordvpn
  rm -f "$SOCK"

  stopping=0
  graceful_stop() {
    stopping=1
    log "stopping: disconnecting VPN and shutting down nordvpnd so host routes and firewall rules are restored"
    timeout 20 nordvpn disconnect >/dev/null 2>&1 || true
    timeout 30 /etc/init.d/nordvpn stop || true
    exit 0
  }
  trap graceful_stop TERM INT

  log "starting nordvpnd ($(nordvpn version 2>/dev/null | head -n1))"
  /etc/init.d/nordvpn start

  if ! wait_for_daemon 60; then
    log "nordvpnd did not come up within 60s"
    exit 1
  fi
  /usr/local/bin/bootstrap.sh || log "bootstrap finished with warnings"
  log "ready"

  # Keep PID 1 alive while nordvpnd runs; `sleep & wait` lets the trap fire immediately.
  while [ "$stopping" = 0 ]; do
    if ! pgrep -x nordvpnd >/dev/null; then
      log "nordvpnd exited, restarting container"
      exit 1
    fi
    sleep 10 & wait $!
  done
}

run_panel() {
  if ! wait_for_daemon 120; then
    log "daemon socket not ready yet, starting panel anyway (it shows the daemon as unavailable)"
  fi
  exec /usr/local/bin/nordpanel -listen "0.0.0.0:${PANEL_PORT:-8080}"
}

case "${1:-daemon}" in
  daemon) run_daemon ;;
  panel)  run_panel ;;
  *)      exec "$@" ;;
esac
