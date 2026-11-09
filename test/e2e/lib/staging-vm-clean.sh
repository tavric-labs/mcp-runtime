#!/usr/bin/env bash
# VM hygiene for the disposable Staging E2E runner: stale k3s containerd shims
# and a pre-cell disk guard. Sourced by test/e2e/staging-vm.sh after the
# disposable-target guard; offline tests live in test/e2e/staging_lib_test.sh.
#
# Scope: only k3s state (processes whose args show the k8s.io containerd
# namespace on the /run/k3s socket, and mounts under /run/k3s,
# /var/lib/kubelet, /var/lib/rancher/k3s) and the suite's own temp files. The
# VM also hosts unrelated Docker containers (containerd namespace "moby",
# socket /run/containerd): they are never signalled or unmounted.

# One "<pid> <args>" line per process. Overridable for tests.
staging_process_table() { ps -eo pid=,args= 2>/dev/null; }
staging_signal_pid() { kill "-$1" "$2" 2>/dev/null; }
staging_umount() { umount "$1" 2>/dev/null || umount -l "$1" 2>/dev/null; }
STAGING_MOUNTS_FILE="${STAGING_MOUNTS_FILE:-/proc/mounts}"

# PIDs of containerd shims that belong to k3s. k3s runs its own containerd
# with `-namespace k8s.io -address /run/k3s/containerd/containerd.sock`;
# Docker's shims use `-namespace moby` and /run/containerd/containerd.sock.
# Both markers must be present.
staging_k3s_shim_pids() {
  staging_process_table | awk '
    index($0, "containerd-shim") && index($0, "-namespace k8s.io") && index($0, "-address /run/k3s/") { print $1 }'
}

staging_kill_stale_k3s_shims() {
  local pids signal
  for signal in TERM KILL; do
    pids="$(staging_k3s_shim_pids | tr '\n' ' ')"
    [[ -n "${pids// /}" ]] || return 0
    staging_log "stopping stale k3s containerd shims (${signal}): ${pids}"
    # shellcheck disable=SC2086 # pid list
    for pid in ${pids}; do staging_signal_pid "${signal}" "${pid}" || true; done
    sleep "${STAGING_SHIM_WAIT:-3}"
  done
  pids="$(staging_k3s_shim_pids | tr '\n' ' ')"
  [[ -z "${pids// /}" ]] || {
    staging_err "k3s containerd shims survived SIGKILL: ${pids}"
    return 1
  }
}

# Unmount leftovers of k3s, deepest first, as k3s-killall.sh does.
staging_unmount_k3s_mounts() {
  local mountpoint rc=0
  while IFS= read -r mountpoint; do
    [[ -n "${mountpoint}" ]] || continue
    staging_umount "${mountpoint}" || {
      staging_err "could not unmount ${mountpoint}"
      rc=1
    }
  done < <(awk '$2 ~ "^/run/k3s(/|$)" || $2 ~ "^/var/lib/kubelet(/|$)" || $2 ~ "^/var/lib/rancher/k3s(/|$)" { print $2 }' "${STAGING_MOUNTS_FILE}" | sort -r)
  return "${rc}"
}

# After a k3s uninstall (or before an install) no k3s shim or mount may remain.
staging_clean_k3s_leftovers() {
  local killall="${STAGING_K3S_KILLALL:-/usr/local/bin/k3s-killall.sh}"
  if [[ -x "${killall}" ]]; then
    "${killall}" >/dev/null 2>&1 || true
  fi
  staging_kill_stale_k3s_shims || return 1
  staging_unmount_k3s_mounts || return 1
}

# ---------------------------------------------------------------------------
# Disk guard
# ---------------------------------------------------------------------------

# "<avail-KiB> <used-percent> <mountpoint>" of the filesystem holding a path
# (or its nearest existing ancestor). Overridable for tests.
staging_df_line() {
  local path="$1"
  while [[ ! -e "${path}" && "${path}" != / ]]; do path="$(dirname "${path}")"; done
  df -Pk "${path}" 2>/dev/null | awk 'NR==2 { gsub("%", "", $5); print $4, $5, $6 }'
}

staging_disk_paths() {
  printf '%s\n' /var/lib/rancher/k3s /var/lib/docker "$(dirname "${WORK_DIR:-/var/tmp/x}")"
}

# Prints one line per filesystem that is too full; returns 1 if any.
staging_disk_short() {
  local min_kib=$((${E2E_MIN_FREE_GIB:-15} * 1024 * 1024)) max_pct="${E2E_MAX_USED_PCT:-80}"
  local path avail pct mount seen=" " rc=0
  while IFS= read -r path; do
    read -r avail pct mount < <(staging_df_line "${path}")
    [[ -n "${mount:-}" && "${seen}" != *" ${mount} "* ]] || continue
    seen+="${mount} "
    if ((avail < min_kib || pct > max_pct)); then
      printf '%s (holds %s): %d GiB free, %s%% used; need >= %s GiB free and <= %s%% used\n' \
        "${mount}" "${path}" $((avail / 1024 / 1024)) "${pct}" "${E2E_MIN_FREE_GIB:-15}" "${max_pct}"
      rc=1
    fi
  done < <(staging_disk_paths)
  return "${rc}"
}

# Suite-owned garbage only: this run's predecessors' temp dirs and archives,
# then unused Docker images (never ones a container uses) and capped build cache.
staging_prune_suite_garbage() {
  local dir
  for dir in /var/tmp/mcp-runtime-e2e-*; do
    [[ -d "${dir}" && "${dir}" != "${WORK_DIR:-}" ]] || continue
    rm -rf -- "${dir}"
  done
  rm -f /tmp/mcp-runtime-e2e.tgz
  if [[ -n "${ROOT_DIR:-}" ]]; then rm -f "${ROOT_DIR}"/mcp-img-*.tar; fi
  staging_prune_docker_keeping_qa_images || true
}

staging_disk_guard() {
  local short
  staging_log "disk before cell: $(df -hP / | awk 'NR==2 {print $4 " free, " $5 " used"}')"
  short="$(staging_disk_short)" || {
    staging_warn "low disk, pruning suite-owned garbage first: ${short}"
    staging_prune_suite_garbage
    short="$(staging_disk_short)" || {
      staging_err "not enough disk to start a cell (it would die of DiskPressure): ${short}"
      staging_err "free space outside the suite, e.g. remove unused Docker images/volumes of finished projects or old logs; never remove running containers"
      return 1
    }
  }
}
