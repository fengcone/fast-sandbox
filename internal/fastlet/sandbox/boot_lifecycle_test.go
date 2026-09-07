package sandbox

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	fastletapi "fast-sandbox/internal/protocol/fastlet"

	"github.com/stretchr/testify/require"
)

// asyncBootRuntime is a MockRuntime with an ImageDelivery switch: the test
// controls whether DeliverImage reports Delivering, Delivered, or an error.
type asyncBootRuntime struct {
	*MockRuntime

	mu            sync.Mutex
	deliverStatus ImageDeliveryStatus
	deliverErr    error
	deliverCalls  int
}

func newAsyncBootRuntime(status ImageDeliveryStatus) *asyncBootRuntime {
	return &asyncBootRuntime{MockRuntime: NewMockRuntime(), deliverStatus: status}
}

func (r *asyncBootRuntime) DeliverImage(context.Context, string) (ImageDeliveryStatus, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliverCalls++
	// The real driver parks the create first and only reports an attempt
	// failure on a later poll; mirror that by reporting Delivering on the
	// first call even when the fake delivery is about to fail.
	if r.deliverCalls == 1 && r.deliverErr != nil && r.deliverStatus == ImageDelivering {
		return ImageDelivering, nil
	}
	return r.deliverStatus, r.deliverErr
}

func (r *asyncBootRuntime) setDeliverStatus(status ImageDeliveryStatus) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliverStatus = status
}

func (r *asyncBootRuntime) setDeliverErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.deliverErr = err
}

func (r *asyncBootRuntime) deliveryPollCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.deliverCalls
}

// InspectSandbox mirrors the Firecracker driver: a runtime that never booted
// reports not-found so the manager message (not a fake "unknown" phase) is
// projected.
func (r *asyncBootRuntime) InspectSandbox(_ context.Context, sandboxID string) (*SandboxMetadata, error) {
	if !r.MockRuntime.HasSandbox(sandboxID) {
		return nil, ErrSandboxNotFound
	}
	return r.MockRuntime.InspectSandbox(context.Background(), sandboxID)
}

func waitForSandboxState(t *testing.T, manager *SandboxManager, sandboxID string, check func(fastletapi.SandboxStatus) bool) fastletapi.SandboxStatus {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		for _, status := range manager.GetSandboxStatuses(context.Background()) {
			if status.SandboxID == sandboxID && check(status) {
				return status
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Sandbox %s did not reach the expected state within 5s (statuses: %+v)", sandboxID, manager.GetSandboxStatuses(context.Background()))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitForSandboxGone(t *testing.T, manager *SandboxManager, sandboxID string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		gone := true
		for _, status := range manager.GetSandboxStatuses(context.Background()) {
			if status.SandboxID == sandboxID {
				gone = false
				break
			}
		}
		if gone {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Sandbox %s was not removed within 5s", sandboxID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestCreateDeliveredImageBootsSynchronously(t *testing.T) {
	runtime := newAsyncBootRuntime(ImageDelivered)
	manager := NewSandboxManager(runtime)

	response, err := ensureSandboxForTest(context.Background(), manager, runtimeSpecForTest("sb-hot", "sb-hot", "img-hot"))
	require.NoError(t, err)
	require.Equal(t, fastletapi.CreateDispositionCreated, response.Disposition)
	require.Equal(t, fastletapi.RuntimeStateReady, response.Sandbox.Runtime.State)
	require.True(t, runtime.MockRuntime.GetCreateCalled(), "a delivered image must boot inside the create call")
}

func TestCreateParksColdImageThenBootsAsync(t *testing.T) {
	runtime := newAsyncBootRuntime(ImageDelivering)
	manager := NewSandboxManager(runtime)

	response, err := ensureSandboxForTest(context.Background(), manager, runtimeSpecForTest("sb-cold", "sb-cold", "img-cold"))
	require.NoError(t, err)
	require.Equal(t, fastletapi.CreateDispositionCreated, response.Disposition)
	require.Equal(t, fastletapi.RuntimeStateCreating, response.Sandbox.Runtime.State)
	require.Contains(t, response.Sandbox.Runtime.Message, "img-cold")
	require.False(t, runtime.MockRuntime.GetCreateCalled(), "a cold create must return before the runtime boots")

	// The artifact arrives: the boot worker must pick it up and commit the
	// runtime exactly like the synchronous path.
	runtime.setDeliverStatus(ImageDelivered)
	status := waitForSandboxState(t, manager, "sb-cold", func(s fastletapi.SandboxStatus) bool {
		return s.Runtime.State == fastletapi.RuntimeStateReady
	})
	require.Equal(t, fastletapi.DataPlaneStateReady, status.DataPlane.State)
	require.True(t, runtime.MockRuntime.GetCreateCalled())
	require.GreaterOrEqual(t, runtime.deliveryPollCount(), 2, "the boot worker must poll the delivery state")
}

func TestCreateParksThenDeleteWhileDeliveringRemovesSandbox(t *testing.T) {
	runtime := newAsyncBootRuntime(ImageDelivering)
	manager := NewSandboxManager(runtime)

	response, err := ensureSandboxForTest(context.Background(), manager, runtimeSpecForTest("sb-del", "sb-del", "img-del"))
	require.NoError(t, err)
	require.Equal(t, fastletapi.RuntimeStateCreating, response.Sandbox.Runtime.State)

	_, err = deleteSandboxForTest(manager, "sb-del")
	require.NoError(t, err)
	waitForSandboxGone(t, manager, "sb-del")
	require.True(t, runtime.MockRuntime.GetDeleteCalled(), "the boot worker must hand a parked Sandbox to the delete path")
}

func TestCreateDeliveryFailureProjectsTerminalStateAndReleasesCapacity(t *testing.T) {
	runtime := newAsyncBootRuntime(ImageDelivering)
	runtime.setDeliverErr(errors.New("sandbox image not published"))
	manager := NewSandboxManager(runtime)

	response, err := ensureSandboxForTest(context.Background(), manager, runtimeSpecForTest("sb-fail", "sb-fail", "img-missing"))
	require.NoError(t, err)
	require.Equal(t, fastletapi.RuntimeStateCreating, response.Sandbox.Runtime.State)

	status := waitForSandboxState(t, manager, "sb-fail", func(s fastletapi.SandboxStatus) bool {
		return s.Runtime.State == fastletapi.RuntimeStateFailed
	})
	require.Contains(t, status.Runtime.Message, "not published")
	require.False(t, runtime.MockRuntime.GetCreateCalled(), "a failed delivery must never boot the runtime")

	// The failed Sandbox occupies no capacity: capacity-1 admission accepts a
	// second Sandbox once the failing image's delivery state is cleared.
	runtime2 := newAsyncBootRuntime(ImageDelivering)
	runtime2.setDeliverErr(errors.New("sandbox image not published"))
	full, err := NewSandboxManagerWithConfig(runtime2, SandboxManagerConfig{Capacity: 1})
	require.NoError(t, err)
	defer func() {
		if full != nil {
			_ = full.runtime.Close()
		}
	}()
	first, err := ensureSandboxForTest(context.Background(), full, runtimeSpecForTest("one", "one", "img-one"))
	require.NoError(t, err)
	require.Equal(t, fastletapi.RuntimeStateCreating, first.Sandbox.Runtime.State)
	waitForSandboxState(t, full, "one", func(s fastletapi.SandboxStatus) bool { return s.Runtime.State == fastletapi.RuntimeStateFailed })

	runtime2.setDeliverErr(nil)
	runtime2.setDeliverStatus(ImageDelivered)
	second, err := ensureSandboxForTest(context.Background(), full, runtimeSpecForTest("two", "two", "img-two"))
	require.NoError(t, err)
	require.Equal(t, fastletapi.CreateDispositionCreated, second.Disposition)
	require.Equal(t, fastletapi.RuntimeStateReady, second.Sandbox.Runtime.State)
}
