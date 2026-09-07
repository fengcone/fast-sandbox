package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	apiv1alpha2 "fast-sandbox/api/v1alpha2"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/klog/v2"
)

// snapshotPhaseTimings records the sub-phase durations of the snapshot stage
// so the builder can surface them in its aggregate timing log line.
type snapshotPhaseTimings struct {
	BootToReadyMs        int64 `json:"bootToReadyMs"`
	SnapshotCreateMs     int64 `json:"snapshotCreateMs"`
	RestoreToHeartbeatMs int64 `json:"restoreToHeartbeatMs"`
}

// buildTap is the host tap backing the baked NIC while the snapshot VM
// runs. It only lives for the snapshot stage; consumers of the snapshot
// replace the host tap per instance via network_overrides, so the tap name
// itself never leaks into the product.
const buildTap = "fc-build-tap"

// The baked guest network (mirrors the driver E2E prep recipe, e2ePrepMAC /
// e2ePrepBootArgs): the snapshot carries a static eth0 configuration, so a
// restored instance owns its address without any kernel ip= args (the
// snapshot is authoritative). Consumers can only replace the host tap name.
const (
	bakedGuestMAC     = "02:00:00:00:00:01"
	bakedGuestIP      = "172.30.0.3"
	bakedGuestGateway = "172.30.0.1"
	bakedGuestNetmask = "255.255.255.0"
)

// runSnapshotStage drives a Firecracker VM from cold boot to a validated
// full snapshot. It is invoked by the builder image as the snapshot stage
// helper:
//
//	sandbox-snapshot-stage <kernel> <rootfs> <vmstate> <memory>
//
// The SandboxTemplate spec arrives in SANDBOX_TEMPLATE_SPEC; the stage waits
// for the guest's SANDBOX_READY marker, pauses, creates a full snapshot, and
// restores it once for validation.
func runSnapshotStage(args []string) error {
	if len(args) != 4 {
		return fmt.Errorf("usage: sandbox-snapshot-stage <kernel> <rootfs> <vmstate> <memory>")
	}
	kernel, rootfs, vmstate, memory := args[0], args[1], args[2], args[3]

	payload := os.Getenv(specEnv)
	if payload == "" {
		return fmt.Errorf("SANDBOX_TEMPLATE_SPEC is required")
	}
	var spec apiv1alpha2.SandboxTemplateSpec
	if err := json.Unmarshal([]byte(payload), &spec); err != nil {
		return fmt.Errorf("parse template spec: %w", err)
	}

	workdir := os.Getenv(workDirEnv)
	if workdir == "" {
		workdir = "/build"
	}

	// The baked NIC needs a host tap as its backing device while the VM
	// runs. It is kept alive through the restore validation (the snapshot's
	// NIC host_dev references it) and removed afterwards.
	if err := ensureBuildTap(); err != nil {
		return err
	}
	defer deleteBuildTap()

	// The drive path baked into the vmstate must be RELATIVE and carry the
	// filename the consumer's restore resolves in its process cwd: the
	// driver restores with cwd = the instance state dir, where the
	// per-instance root drive is named rootfs.img (the reflink copy). A
	// symlink in the workdir gives the snapshot VM the same file under
	// that name (both the snapshot create and the Stage B restore run with
	// cwd = workdir).
	driveLink := filepath.Join(workdir, snapshotDriveName)
	_ = os.Remove(driveLink)
	if err := os.Symlink(rootfs, driveLink); err != nil {
		return fmt.Errorf("link snapshot drive %s: %w", driveLink, err)
	}

	// Stage A: cold boot and wait for guest readiness.
	bootLog := filepath.Join(workdir, "boot.console.log")
	bootSocket := filepath.Join(workdir, "boot.sock")
	phaseStarted := time.Now()
	vm, err := startVMM(bootSocket, bootLog, workdir)
	if err != nil {
		return err
	}
	// random.trust_cpu=on seeds the guest CRNG from RDRAND at boot. The
	// microVM has no other entropy source (no virtio-rng device, and
	// Firecracker snapshots preserve the CPUID mask), so without it the CRNG
	// stays uninitialized and any getrandom()/crypto/rand caller — e.g. execd
	// generating a session id for POST /command — blocks forever. The golden
	// snapshot carries the initialized CRNG, so every restored instance is
	// fixed too.
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off nomodules net.ifnames=0 biosdevname=0 random.trust_cpu=on root=/dev/vda rw"
	bootArgs += " init=" + guestInitPath(spec)
	bootArgs = bakedNetworkBootArgs(bootArgs)
	if err := configureVM(vm, kernel, rootfs, bootArgs, spec); err != nil {
		vm.stop()
		return err
	}
	if err := waitForMarker(bootLog, "SANDBOX_READY", readinessTimeout(spec)); err != nil {
		vm.stop()
		return err
	}
	bootToReadyMs := time.Since(phaseStarted).Milliseconds()

	// Pause and create the full snapshot.
	snapshotStarted := time.Now()
	if err := api(vm.socket, "PATCH", "/vm", map[string]string{"state": "Paused"}); err != nil {
		vm.stop()
		return err
	}
	if err := api(vm.socket, "PUT", "/snapshot/create", map[string]any{
		"snapshot_type": "Full",
		"snapshot_path": vmstate,
		"mem_file_path": memory,
	}); err != nil {
		vm.stop()
		return err
	}
	vm.stop()
	snapshotCreateMs := time.Since(snapshotStarted).Milliseconds()

	// Stage B: restore once for validation. The restore does not re-apply the
	// machine config / rootfs drive: it relies on the snapshot's embedded
	// device state and the original paths still existing on this host. This
	// is verified against firecracker v1.16.1 but is an implicit dependency
	// on that version's behavior — revisit when bumping the VMM.
	//
	// The baked NIC references buildTap, which is still alive here, so the
	// validation restore needs no network_overrides; consumers later replace
	// the host tap per instance.
	restoreStarted := time.Now()
	restoreLog := filepath.Join(workdir, "restore.console.log")
	restoreSocket := filepath.Join(workdir, "restore.sock")
	restored, err := startVMM(restoreSocket, restoreLog, workdir)
	if err != nil {
		return err
	}
	defer restored.stop()
	if err := api(restored.socket, "PUT", "/snapshot/load", map[string]any{
		"snapshot_path": vmstate,
		"mem_backend": map[string]any{
			"backend_type": "File",
			"backend_path": memory,
		},
		"resume_vm": true,
	}); err != nil {
		return err
	}
	// The guest init does not re-run after a snapshot restore; wait for the
	// heartbeat loop instead of the one-shot readiness marker. The restore
	// timeout scales with the guest memory (the memory image is read back in
	// full, bounded by host storage speed).
	if err := waitForMarker(restoreLog, "SANDBOX_HEARTBEAT", restoreTimeout(spec)); err != nil {
		return err
	}
	restoreToHeartbeatMs := time.Since(restoreStarted).Milliseconds()
	phases := snapshotPhaseTimings{
		BootToReadyMs:        bootToReadyMs,
		SnapshotCreateMs:     snapshotCreateMs,
		RestoreToHeartbeatMs: restoreToHeartbeatMs,
	}
	if payload, err := json.Marshal(phases); err == nil {
		_ = os.WriteFile(filepath.Join(workdir, "snapshot-phases.json"), payload, 0o644)
	}
	klog.InfoS("snapshot stage phases",
		"format", spec.Output.Format,
		"bootToReadyMs", phases.BootToReadyMs,
		"snapshotCreateMs", phases.SnapshotCreateMs,
		"restoreToHeartbeatMs", phases.RestoreToHeartbeatMs)
	_, _ = fmt.Fprintln(os.Stderr, "snapshot stage: boot and restore validation passed")
	return nil
}

// vmm is a managed Firecracker process.
type vmm struct {
	socket  string
	log     string
	process *exec.Cmd
}

// startVMM launches firecracker in the background with the serial output
// captured in the console log, then waits for the API socket.
func startVMM(socketPath, logPath, workdir string) (*vmm, error) {
	// A stale socket file from a crashed or interrupted earlier run makes
	// the firecracker bind fail (EADDRINUSE) and the process exit right
	// after startup; remove it before launching.
	_ = os.Remove(socketPath)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	command := exec.Command(firecrackerBin, "--api-sock", socketPath)
	command.Dir = workdir
	command.Stdout = logFile
	command.Stderr = logFile
	if err := command.Start(); err != nil {
		_ = logFile.Close()
		return nil, err
	}
	vm := &vmm{socket: socketPath, log: logPath, process: command}
	if err := waitForFile(socketPath, 15*time.Second); err != nil {
		vm.stop()
		return nil, err
	}
	return vm, nil
}

func (vm *vmm) stop() {
	if vm.process != nil && vm.process.Process != nil {
		_ = vm.process.Process.Kill()
		_ = vm.process.Wait()
	}
}

// configureVM applies the machine config, boot source, root drive, the baked
// guest NIC, and starts the instance. The NIC (iface eth0, MAC, and the
// static guest address from the kernel ip= boot args) is baked into the
// snapshot, so every restored instance resumes with the same guest network
// (clone networking model); consumers override only the host tap name via
// network_overrides.
func configureVM(vm *vmm, kernel, rootfs, bootArgs string, spec apiv1alpha2.SandboxTemplateSpec) error {
	vcpuCount, err := vcpus(spec.Machine.VCPU)
	if err != nil {
		return err
	}
	memSize, err := memoryMiB(spec.Machine.Memory)
	if err != nil {
		return err
	}
	// The cpu_template is pinned so the guest's CPUID is identical on every
	// host: a full snapshot restored on a machine with different CPU
	// features can fail or misbehave. T2 is the conservative cross-vendor
	// baseline (fixed CPUID masking).
	if err := api(vm.socket, "PUT", "/machine-config", map[string]any{
		"vcpu_count":   vcpuCount,
		"mem_size_mib": memSize,
		"smt":          false,
		"cpu_template": "T2",
	}); err != nil {
		return err
	}
	// Entropy device (virtio-rng): the microVM has no other entropy source —
	// the T2 CPU template masks RDRAND/RDSEED (snapshot determinism) and no
	// host RNG device is passed through — so without it the guest CRNG never
	// initializes and getrandom() callers (Go crypto/rand, uuid, openssl,
	// python secrets, TLS) block forever; execd POST /command hangs on its
	// session id while /ping and malformed-JSON 400 stay instant (OpenSandbox
	// #1695). Seeding the CRNG at prep boot means the golden snapshot carries
	// an initialized CRNG and every restored instance works.
	if err := api(vm.socket, "PUT", "/entropy", map[string]any{}); err != nil {
		return fmt.Errorf("attach entropy device: %w", err)
	}
	if err := api(vm.socket, "PUT", "/boot-source", map[string]any{
		"kernel_image_path": kernel,
		"boot_args":         bootArgs,
	}); err != nil {
		return err
	}
	if err := api(vm.socket, "PUT", "/drives/rootfs", map[string]any{
		"drive_id":       "rootfs",
		"path_on_host":   snapshotDriveName,
		"is_root_device": true,
		"is_read_only":   false,
	}); err != nil {
		return err
	}
	if err := api(vm.socket, "PUT", "/network-interfaces/eth0", map[string]any{
		"iface_id":      "eth0",
		"host_dev_name": buildTap,
		"guest_mac":     bakedGuestMAC,
	}); err != nil {
		return err
	}
	return api(vm.socket, "PUT", "/actions", map[string]string{"action_type": "InstanceStart"})
}

// snapshotDriveName is the RELATIVE drive path baked into the vmstate. It
// must match the filename the driver's restore resolves in its process cwd:
// the per-instance rootfs reflink copy is rootfs.img in the instance state
// directory (the driver's instanceRootfsName).
const snapshotDriveName = "rootfs.img"

// ensureBuildTap creates the host tap backing the baked NIC and brings it
// up with the baked gateway address on it.
//
// The tap MUST be UP or the tun driver answers every guest TX with EIO
// (writes to a down tap return -EIO): during the bake the guest's
// network-dependent bootstrap (execd resolving/announcing through its
// baked gateway/DNS 172.30.0.1) then fails and SANDBOX_READY never
// appears. This mirrors the runtime per-clone tap setup
// (guest_vm_linux_driver.go): the tap is brought up and owns the gateway
// address so the guest can resolve 172.30.0.1 on the L2 segment.
func ensureBuildTap() error {
	if output, err := exec.Command("ip", "tuntap", "add", "dev", buildTap, "mode", "tap").CombinedOutput(); err != nil {
		return fmt.Errorf("create build tap %s: %w: %s", buildTap, err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("ip", "link", "set", buildTap, "up").CombinedOutput(); err != nil {
		return fmt.Errorf("bring build tap %s up: %w: %s", buildTap, err, strings.TrimSpace(string(output)))
	}
	if output, err := exec.Command("ip", "addr", "replace", bakedGuestGateway+"/24", "dev", buildTap).CombinedOutput(); err != nil {
		return fmt.Errorf("assign gateway %s to build tap %s: %w: %s", bakedGuestGateway, buildTap, err, strings.TrimSpace(string(output)))
	}
	return nil
}

// deleteBuildTap removes the build tap; a missing tap is not an error.
func deleteBuildTap() {
	_, _ = exec.Command("ip", "link", "del", buildTap).CombinedOutput()
}

// bakedNetworkBootArgs appends the static guest network to the kernel
// command line so the snapshot carries a live eth0 configuration (the
// restored guest owns the address without kernel ip= args — the snapshot
// is authoritative).
func bakedNetworkBootArgs(base string) string {
	if strings.Contains(base, " ip=") {
		return base
	}
	return base + " ip=" + bakedGuestIP + "::" + bakedGuestGateway + ":" + bakedGuestNetmask + "::eth0:off"
}

// api performs one Firecracker API call over its Unix domain socket. A
// deadline covers the whole exchange so a hung VMM fails cleanly instead of
// blocking the build forever.
func api(socketPath, method, path string, body any) error {
	var payload []byte
	var err error
	if body != nil {
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	connection, err := net.DialTimeout("unix", socketPath, 10*time.Second)
	if err != nil {
		return fmt.Errorf("dial firecracker socket: %w", err)
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(apiTimeout)); err != nil {
		return err
	}
	request, err := http.NewRequest(method, "http://localhost"+path, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if err := request.Write(connection); err != nil {
		return fmt.Errorf("firecracker API request %s %s: %w", method, path, err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		return fmt.Errorf("firecracker API response %s %s: %w", method, path, err)
	}
	defer response.Body.Close()
	content, _ := io.ReadAll(response.Body)
	if response.StatusCode >= 400 {
		return fmt.Errorf("firecracker API failed: %s %s -> %d: %s", method, path, response.StatusCode, strings.TrimSpace(string(content)))
	}
	return nil
}

// apiTimeout bounds a single Firecracker API exchange (snapshot create/load
// of a multi-GiB memory image can legitimately take a while).
const apiTimeout = 5 * time.Minute

func waitForFile(path string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", path)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// waitForMarker polls a log file for a marker, reading only the appended
// bytes since the last poll (the heartbeat loop keeps growing the file).
// The previous poll's tail is kept so a marker split across two reads (the
// console writes are not atomic) still matches. Returns the tail of the
// file on timeout for diagnostics.
func waitForMarker(logPath, marker string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var offset int64
	var previousTail []byte
	overlap := 4096
	if len(marker) > overlap {
		overlap = len(marker)
	}
	for {
		if payload, newOffset, ok := readSince(logPath, offset); ok {
			offset = newOffset
			window := payload
			if len(previousTail) > 0 {
				window = append(append([]byte{}, previousTail...), payload...)
			}
			if bytes.Contains(window, []byte(marker)) {
				return nil
			}
			if len(payload) >= overlap {
				previousTail = payload[len(payload)-overlap:]
			} else {
				previousTail = append(previousTail, payload...)
				if len(previousTail) > overlap {
					previousTail = previousTail[len(previousTail)-overlap:]
				}
			}
		}
		if time.Now().After(deadline) {
			tail := ""
			if payload, err := os.ReadFile(logPath); err == nil {
				if len(payload) > 4096 {
					payload = payload[len(payload)-4096:]
				}
				tail = string(payload)
			}
			return fmt.Errorf("timed out waiting for %q in %s\n%s", marker, logPath, tail)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// readSince returns the bytes appended to path after offset (the file may
// have been recreated or truncated, in which case reading restarts from the
// beginning).
func readSince(path string, offset int64) ([]byte, int64, bool) {
	handle, err := os.Open(path)
	if err != nil {
		return nil, offset, false
	}
	defer handle.Close()
	if stat, err := handle.Stat(); err == nil && stat.Size() < offset {
		// Truncated/recreated: restart from the top.
		offset = 0
	}
	if _, err := handle.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, false
	}
	payload, err := io.ReadAll(handle)
	if err != nil {
		return nil, offset, false
	}
	return payload, offset + int64(len(payload)), true
}

func readinessTimeout(spec apiv1alpha2.SandboxTemplateSpec) time.Duration {
	timeout := 300 * time.Second
	if spec.Readiness.WarmupSeconds > 0 {
		timeout = time.Duration(spec.Readiness.WarmupSeconds+60) * time.Second
	}
	return timeout
}

// restoreTimeout bounds the restore-to-heartbeat wait. Unlike boot (where
// the warmup dominates), a restore reads the full memory image back, so the
// timeout scales with machine.memory: ~50 MB/s conservative read speed plus
// a 2-minute heartbeat margin, never below the fixed 5-minute floor.
func restoreTimeout(spec apiv1alpha2.SandboxTemplateSpec) time.Duration {
	timeout := 300 * time.Second
	if miB, err := memoryMiB(spec.Machine.Memory); err == nil {
		scaled := time.Duration(miB/50)*time.Second + 120*time.Second
		if scaled > timeout {
			timeout = scaled
		}
	}
	return timeout
}

// vcpus parses the vCPU quantity ("4" or "4000m") into whole cores, with a
// default of 1. Unparsable input is an error rather than a silent fallback.
func vcpus(quantity string) (int, error) {
	if quantity == "" {
		return 1, nil
	}
	parsed, err := resource.ParseQuantity(quantity)
	if err != nil {
		return 0, fmt.Errorf("invalid vcpu quantity %q: %w", quantity, err)
	}
	cores := parsed.Value()
	if cores < 1 || cores > firecrackerMaxVCPUs {
		return 0, fmt.Errorf("vcpu quantity %q out of range [1,%d]", quantity, firecrackerMaxVCPUs)
	}
	return int(cores), nil
}

// firecrackerMaxVCPUs is the Firecracker v1.16.1 MAX_SUPPORTED_VCPUS limit.
const firecrackerMaxVCPUs = 32

// memoryMiB parses the memory quantity ("1Gi") into MiB, with a floor of 128.
// Unparsable input is an error rather than a silent fallback.
func memoryMiB(quantity string) (int64, error) {
	if quantity == "" {
		return 2048, nil
	}
	parsed, err := resource.ParseQuantity(quantity)
	if err != nil {
		return 0, fmt.Errorf("invalid memory quantity %q: %w", quantity, err)
	}
	miB := parsed.Value() / (1024 * 1024)
	if miB < 128 {
		return 128, nil
	}
	return miB, nil
}
