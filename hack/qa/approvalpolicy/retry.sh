#!/usr/bin/env bash
# Retry a command whose dependency (for example an admission webhook that a Helm
# release has just started) is not serving yet. A Deployment reporting Ready does
# not mean its webhook accepts calls, so a bounded retry replaces a blind sleep.
#
# Usage: retry_until <attempts> <command> [args...]
# Locals are retry_-prefixed because bash scoping would otherwise shadow a
# caller's variables. RETRY_DELAY (seconds, default 3) separates attempts. Returns the last exit code
# once the attempt budget is spent, so a permanent failure is never hidden.
retry_until() {
  local retry_max="$1" retry_n retry_rc=1
  shift
  for ((retry_n = 1; retry_n <= retry_max; retry_n++)); do
    "$@" && return 0
    retry_rc=$?
    if ((retry_n < retry_max)); then
      echo "attempt $retry_n/$retry_max failed (exit $retry_rc); retrying in ${RETRY_DELAY:-3}s: $1" >&2
      sleep "${RETRY_DELAY:-3}"
    fi
  done
  return "$retry_rc"
}
