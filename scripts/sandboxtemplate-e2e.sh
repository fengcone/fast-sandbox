#!/usr/bin/env bash
# sandboxtemplate-e2e.sh — exercise the SandboxTemplate conversion core
# (cmd/sandboxtemplate-builder) on a real KVM host, without Kubernetes.
#
# The default path builds the builder image from build/Dockerfile.sandboxtemplate-builder
# and runs the pipeline inside a container (privileged, /dev/kvm, the test
# image tarball mounted read-only, the workspace mounted as /build):
#
#   OCI image (docker save tarball) → ext4 rootfs with the OpenSandbox
#   runtime injected → cold boot → guest readiness → full snapshot →
#   restore validation → manifest + SHA256SUMS.
#
# With --local the builder binary is compiled on the host and the pipeline
# runs directly against the host toolchain instead.
#
# Requirements:
#   - Linux x86_64 with /dev/kvm (root; the script re-invokes itself with sudo)
#   - docker (to export the test image and run the builder container),
#     go toolchain (--local mode), e2fsprogs, jq (manifest display/assertions)
#
# Usage:
#   ./scripts/sandboxtemplate-e2e.sh [--image <ref|tar>] [--format native|overlaybd] [--local]

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK="${WORK:-$PWD/.sandboxtemplate-e2e}"
IMAGE="${SANDBOX_TEMPLATE_IMAGE:-alpine:3.19}"
# Formats to exercise; defaults to both. --format may be repeated or a
# comma-separated list to run a subset.
FORMATS=()
BUILDER_IMAGE="${SANDBOX_TEMPLATE_BUILDER_IMAGE:-sandboxtemplate-builder:e2e}"
KERNEL_URL="${SANDBOX_TEMPLATE_KERNEL_URL:-https://s3.amazonaws.com/spec.ccfc.min/firecracker-ci/20260722-38359b8055fc-0/x86_64/vmlinux-6.18.36}"
LOCAL_MODE=0

log() { printf '\033[1;34m[st-e2e]\033[0m %s\n' "$*"; }
die() { printf '\033[1;31m[st-e2e] ERROR:\033[0m %s\n' "$*" >&2; exit 1; }

while [[ $# -gt 0 ]]; do
    case "$1" in
        --image) IMAGE="${2:?--image requires a value}"; shift 2 ;;
        --format)
            value="${2:?--format requires a value}"; shift 2
            IFS=',' read -r -a parts <<< "$value"
            for part in "${parts[@]}"; do
                [[ "$part" == "native" || "$part" == "overlaybd" ]] || die "--format must be native or overlaybd, got $part"
                FORMATS+=("$part")
            done ;;
        --local) LOCAL_MODE=1; shift ;;
        *) die "unknown argument: $1" ;;
    esac
done
if [[ ${#FORMATS[@]} -eq 0 ]]; then
    FORMATS=(native overlaybd)
fi

# --- re-invoke as root -------------------------------------------------------
if [[ "$(id -u)" -ne 0 ]]; then
    if command -v sudo >/dev/null; then
        log "re-invoking as root (sudo -E)"
        exec sudo -E env "PATH=$PATH" "WORK=$WORK" "SANDBOX_TEMPLATE_IMAGE=$IMAGE" \
            "SANDBOX_TEMPLATE_KERNEL_URL=$KERNEL_URL" "$0" "$@"
    fi
    die "must run as root (or install sudo)"
fi

[[ "$(uname -m)" == "x86_64" ]] || die "requires x86_64"
[[ -e /dev/kvm ]] || die "/dev/kvm is missing"
command -v docker >/dev/null || die "missing required command: docker"
command -v go >/dev/null || die "missing required command: go (for --local mode and spec checks)"
command -v jq >/dev/null || die "missing required command: jq (manifest assertions)"

log "workspace: $WORK (formats=${FORMATS[*]}, local=$LOCAL_MODE)"
rm -rf "$WORK"
mkdir -p "$WORK/input"

# --- test image --------------------------------------------------------------
IMAGE_TAR="$WORK/input/image.tar"
if [[ "$IMAGE" != *.tar ]]; then
    log "exporting test image $IMAGE"
    docker pull -q "$IMAGE" >/dev/null 2>&1 || die "docker pull failed: $IMAGE"
    docker save "$IMAGE" -o "$IMAGE_TAR" >/dev/null 2>&1 || die "docker save failed"
else
    cp "$IMAGE" "$IMAGE_TAR"
fi

# --- runner ------------------------------------------------------------------
if [[ "$LOCAL_MODE" -eq 1 ]]; then
    command -v oci2rootfs >/dev/null || die "missing oci2rootfs on PATH (or use the docker mode)"
    command -v firecracker >/dev/null || die "missing firecracker on PATH (or use the docker mode)"
    if [[ "${FORMATS[*]}" == *overlaybd* ]]; then
        command -v overlaybd-import-raw >/dev/null || die "missing overlaybd-import-raw on PATH (needed for format=overlaybd; use the docker mode)"
    fi
    log "building sandboxtemplate-builder (local mode)"
    (cd "$REPO_ROOT" && GOTOOLCHAIN=local go build -o "$WORK/sandboxtemplate-builder" ./cmd/sandboxtemplate-builder/)
    run_pipeline() {
        local fmt_dir=$1
        env SANDBOX_TEMPLATE_SPEC="$(cat "$fmt_dir/spec.json")" \
            SANDBOX_TEMPLATE_WORKDIR="$fmt_dir/build" \
            SANDBOX_TEMPLATE_IMAGE_TAR="$IMAGE_TAR" \
            SANDBOX_TEMPLATE_ALLOW_NO_PUBLISH=1 \
            POD_NAME=e2e-pod POD_NAMESPACE=default \
            "$WORK/sandboxtemplate-builder"
    }
else
    log "building builder image from build/Dockerfile.sandboxtemplate-builder"
    docker build --progress=plain --build-arg "KERNEL_URL=$KERNEL_URL" -t "$BUILDER_IMAGE" \
        -f "$REPO_ROOT/build/Dockerfile.sandboxtemplate-builder" "$REPO_ROOT" || die "docker build failed"
    run_pipeline() {
        local fmt_dir=$1
        docker run --rm \
            --privileged \
            --device /dev/kvm:/dev/kvm \
            --device /dev/net/tun:/dev/net/tun \
            -v "$WORK/input":/input:ro \
            -v "$fmt_dir/build":/build \
            -e SANDBOX_TEMPLATE_SPEC="$(cat "$fmt_dir/spec.json")" \
            -e SANDBOX_TEMPLATE_WORKDIR=/build \
            -e SANDBOX_TEMPLATE_IMAGE_TAR=/input/image.tar \
            -e SANDBOX_TEMPLATE_ALLOW_NO_PUBLISH=1 \
            -e POD_NAME=e2e-pod -e POD_NAMESPACE=default \
            "$BUILDER_IMAGE"
    }
fi

# --- run the pipeline per format ---------------------------------------------
overall=0
for fmt in "${FORMATS[@]}"; do
    FMT_DIR="$WORK/$fmt"
    mkdir -p "$FMT_DIR/build"
    printf '{
  "image": "%s",
  "entrypoint": ["/bin/sh"],
  "kernel": "vmlinux.bin",
  "machine": {"vcpu": "2", "memory": "1Gi"},
  "init": "/usr/local/sbin/sandbox-init",
  "envs": [{"name": "E2E", "value": "1"}],
  "readiness": {"warmupSeconds": 15},
  "output": {"rootfsSize": "10Gi", "format": "%s"}
}
' "$IMAGE" "$fmt" > "$FMT_DIR/spec.json"
    log "running the conversion pipeline (format=$fmt)"
    set +e
    run_pipeline "$FMT_DIR" 2> "$FMT_DIR/pipeline.log"
    status=$?
    set -e
    if [[ $status -ne 0 ]]; then
        echo "=== pipeline.log (tail, format=$fmt) ===" >&2
        tail -120 "$FMT_DIR/pipeline.log" >&2
        overall=1
        continue
    fi

    BUILD="$FMT_DIR/build"
    # Assertions are recorded (not fatal) so a broken format does not abort
    # the remaining formats; the final exit code is non-zero if any failed.
    assert() {
        if [[ $# -lt 2 ]]; then die "assert: usage <description> <command...>"; fi
        local description=$1; shift
        if ! "$@" >/dev/null 2>&1; then
            echo "  FAIL: $description (format=$fmt)" >&2
            overall=1
        fi
    }
    assert "rootfs.ext4 exists" test -s "$BUILD/rootfs.ext4"
    assert "vmstate.snap exists" test -s "$BUILD/vmstate.snap"
    assert "memory.snap exists" test -s "$BUILD/memory.snap"
    assert "manifest.json exists" test -s "$BUILD/manifest.json"
    assert "SHA256SUMS exists" test -s "$BUILD/SHA256SUMS"
    assert "guest reached readiness" grep -q "SANDBOX_READY" "$BUILD/boot.console.log"
    assert "snapshot restore produced a guest heartbeat" grep -q "SANDBOX_HEARTBEAT" "$BUILD/restore.console.log"
    assert "manifest records the baked guest network" jq -e '.guestNetwork.iface == "eth0" and .guestNetwork.ip == "172.30.0.3" and .guestNetwork.mac == "02:00:00:00:00:01" and .guestNetwork.gateway == "172.30.0.1"' "$BUILD/manifest.json"
    assert "boot args bake the static guest IP" grep -q "ip=172.30.0.3::172.30.0.1:255.255.255.0::eth0:off" "$BUILD/boot.console.log"
    if [[ "$fmt" == "overlaybd" ]]; then
        assert "overlaybd rootfs layer exists" test -s "$BUILD/overlaybd/rootfs/layer.lsmt"
        assert "overlaybd memory layer exists" test -s "$BUILD/overlaybd/memory/layer.lsmt"
    fi

    # Cross-check the builder's sparse-aware digests against sha256sum so a
    # hasher regression cannot pass silently.
    while IFS= read -r path; do
        [[ -f "$BUILD/$path" ]] || { echo "  FAIL: manifest file missing: $path (format=$fmt)" >&2; overall=1; continue; }
        want=$(jq -r --arg p "$path" '.files[$p].sha256' "$BUILD/manifest.json")
        got=$(sha256sum "$BUILD/$path" | awk '{print $1}')
        if [[ "$got" != "$want" ]]; then
            echo "  FAIL: digest mismatch for $path: manifest=$want sha256sum=$got (format=$fmt)" >&2
            overall=1
        fi
    done < <(jq -r '.files | keys[]' "$BUILD/manifest.json")

    log "format=$fmt OK:"
    jq . "$BUILD/manifest.json" 2>/dev/null || cat "$BUILD/manifest.json"
    du -h "$BUILD/rootfs.ext4" "$BUILD/vmstate.snap" "$BUILD/memory.snap" 2>/dev/null || true
    grep -h "SANDBOX_READY" "$BUILD/boot.console.log" | tail -1
done

if [[ $overall -ne 0 ]]; then
    die "one or more formats failed"
fi
log "E2E passed — artifacts under $WORK/<format>/build"

