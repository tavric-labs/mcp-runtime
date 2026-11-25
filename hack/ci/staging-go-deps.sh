#!/usr/bin/env bash
# Prepare public, checksum-verified Go inputs for a guarded disposable VM run.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
mode="${1:-}"
archive="${2:-}"
want="$(awk '/^go [0-9]/ { print $2; exit }' "${ROOT}/go.mod")"
[[ -n "${archive}" && -n "${want}" ]] || { echo 'Usage: staging-go-deps.sh pack <archive> | unpack <archive> <directory>' >&2; exit 2; }

# Never consult a developer's private proxies, modules or Go configuration.
export GOENV=off GOWORK=off GOFLAGS='' GOTOOLCHAIN=local
export GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org
export GOPRIVATE='' GONOPROXY='' GONOSUMDB='' GOINSECURE=''

case "${mode}" in
  pack)
    [[ ! -e "${archive}" && ! -e "${archive}.sha256" ]] || exit 1
    [[ "$(go env GOVERSION)" == "go${want}" && "$(go env GOHOSTOS)" == linux ]] || exit 1
    [[ "$(go env GOHOSTARCH)" == amd64 || "$(go env GOHOSTARCH)" == arm64 ]] || exit 1
    scratch="$(mktemp -d)"
    cleanup_pack() {
      local status=$? cleanup_status=0
      trap - EXIT
      # Go makes extracted modules read-only. Its own cleanup command knows
      # how to remove them and is confined to this private GOMODCACHE.
      GOMODCACHE="${scratch}/modules" go clean -modcache || cleanup_status=$?
      rm -rf "${scratch}" || cleanup_status=$?
      if [[ "${status}" != 0 ]]; then
        exit "${status}"
      fi
      exit "${cleanup_status}"
    }
    trap cleanup_pack EXIT
    export GOMODCACHE="${scratch}/modules"
    mkdir -p "${GOMODCACHE}/cache/download"
    for module in "${ROOT}" "${ROOT}"/services/* "${ROOT}"/traefik-plugins/* "${ROOT}"/examples/*; do
      [[ -f "${module}/go.mod" ]] || continue
      # Downloads authenticate module ZIPs against go.sum or the mandatory
      # checksum database. We package those ZIPs, never extracted sources.
      # go mod verify additionally checks extracted trees and fails on the
      # services' local mcp-runtime replacement (which has no public ZIP).
      (cd "${module}" && go mod download) || exit 1
    done
    cp -R "$(go env GOROOT)" "${scratch}/toolchain"
    # Only public download-cache inputs and the official toolchain belong in
    # the bundle; omit extracted sources, build caches and machine config.
    COPYFILE_DISABLE=1 tar -C "${scratch}" -czf "${scratch}/inputs.tgz" toolchain modules/cache/download
    mkdir -p "$(dirname "${archive}")"
    mv "${scratch}/inputs.tgz" "${archive}"
    python3 - "${archive}" <<'PY'
import hashlib
from pathlib import Path
import sys
path = Path(sys.argv[1])
with path.open('rb') as source:
    digest = hashlib.file_digest(source, 'sha256') if hasattr(hashlib, 'file_digest') else None
    if digest is None:
        digest = hashlib.sha256()
        for chunk in iter(lambda: source.read(1024 * 1024), b''):
            digest.update(chunk)
Path(str(path) + '.sha256').write_text(digest.hexdigest() + '\n')
PY
    ;;
  unpack)
    destination="${3:?missing extraction directory}"
    [[ ! -e "${destination}" ]] || exit 1
    # Validate integrity and every member before creating the run directory.
    # Reject links and special files as well as traversal; these inputs need
    # only ordinary files and directories from our own pack step.
    python3 - "${archive}" "${destination}" <<'PY' || exit 1
import hashlib
from pathlib import Path, PurePosixPath
import sys
import tarfile
path, destination = map(Path, sys.argv[1:])
digest = hashlib.sha256()
with path.open('rb') as source:
    for chunk in iter(lambda: source.read(1024 * 1024), b''):
        digest.update(chunk)
if digest.hexdigest() != Path(str(path) + '.sha256').read_text().strip():
    raise SystemExit('Go input bundle checksum mismatch')
with tarfile.open(path, 'r:gz') as source:
    members = source.getmembers()
    for member in members:
        name = PurePosixPath(member.name)
        if (name.is_absolute() or '..' in name.parts or not name.parts
                or name.parts[0] not in ('toolchain', 'modules')
                or not (member.isfile() or member.isdir())):
            raise SystemExit('Unsafe member in Go input bundle')
    destination.mkdir(parents=True)
    source.extractall(destination, members=members)
PY
    case "$(uname -m)" in
      x86_64) target_arch=amd64 ;;
      aarch64 | arm64) target_arch=arm64 ;;
      *) echo 'Unsupported disposable VM architecture' >&2; exit 1 ;;
    esac
    [[ "$(uname -s)" == Linux ]] || exit 1
    bundled_go="${destination}/toolchain/bin/go"
    [[ "$("${bundled_go}" env GOVERSION)" == "go${want}" ]] || exit 1
    [[ "$("${bundled_go}" env GOHOSTOS)" == linux && "$("${bundled_go}" env GOHOSTARCH)" == "${target_arch}" ]] || exit 1
    ;;
  *) echo "Unknown mode: ${mode}" >&2; exit 2 ;;
esac
