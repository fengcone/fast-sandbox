package firecracker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	runtimecontract "fast-sandbox/internal/runtime/contract"

	"github.com/stretchr/testify/require"
)

// materializingAgent is a fake agent whose PinImage commits the rootfs into
// the shared cache root, mirroring the real agent's pull-commit behavior.
type materializingAgent struct {
	*fakeAgentClient
	cacheRoot string
	mu        sync.Mutex
	pinErr    error
	pinCount  int
}

func (m *materializingAgent) PinImage(ctx context.Context, requestID, image string) (string, error) {
	m.mu.Lock()
	err := m.pinErr
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	digest, err := m.fakeAgentClient.PinImage(ctx, requestID, image)
	if err != nil {
		return "", err
	}
	m.mu.Lock()
	m.pinCount++
	m.mu.Unlock()
	dir := filepath.Join(m.cacheRoot, imageCacheDir, imageKey(image))
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	return digest, os.WriteFile(filepath.Join(dir, rootfsImageName), []byte("rootfs-image-data"), 0o640)
}

func (m *materializingAgent) setPinErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pinErr = err
}

func (m *materializingAgent) deliveredPins() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pinCount
}

// installMaterializingAgent wires a materializing agent into a fixture.
func (f *driverFixture) installMaterializingAgent(agent *materializingAgent) {
	f.driver.newAgentClient = func(string) (AgentClient, error) { return agent, nil }
	f.driver.agentSocket = "/run/fast-sandbox/firecracker/runtime.sock"
	f.driver.podUID = "pod-1"
}

func TestDeliverImageReportsDeliveredWhenCached(t *testing.T) {
	fixture := newDriverFixture(t)
	fixture.prepareCachedImage(t, fixture.sandboxSpec.Spec.Image)

	status, err := fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.NoError(t, err)
	require.Equal(t, runtimecontract.ImageDelivered, status)
}

func TestDeliverImageLocalModeMissingIsImpossible(t *testing.T) {
	fixture := newDriverFixture(t)

	status, err := fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.ErrorIs(t, err, ErrImageNotReady)
	require.Equal(t, runtimecontract.ImageDeliveryStatus(""), status)

	status, err = fixture.driver.DeliverImage(context.Background(), "")
	require.ErrorIs(t, err, ErrInvalidConfig)
	require.Equal(t, runtimecontract.ImageDeliveryStatus(""), status)
}

func TestDeliverImageKicksBackgroundAttemptUntilCommitted(t *testing.T) {
	fixture := newDriverFixture(t)
	agent := &materializingAgent{fakeAgentClient: &fakeAgentClient{}, cacheRoot: fixture.stateRoot}
	fixture.installMaterializingAgent(agent)

	started := time.Now()
	status, err := fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.NoError(t, err)
	require.Equal(t, runtimecontract.ImageDelivering, status)

	require.Eventually(t, func() bool {
		_, resolveErr := resolveRootfsImage(fixture.stateRoot, fixture.sandboxSpec.Spec.Image)
		return resolveErr == nil
	}, 5*time.Second, 20*time.Millisecond, "background delivery must commit the image")

	require.GreaterOrEqual(t, agent.deliveredPins(), 1)
	require.Less(t, time.Since(started), 5*time.Second, "DeliverImage must never block on the transfer")

	status, err = fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.NoError(t, err)
	require.Equal(t, runtimecontract.ImageDelivered, status)
}

func TestDeliverImageIsSingleFlight(t *testing.T) {
	fixture := newDriverFixture(t)
	agent := &materializingAgent{fakeAgentClient: &fakeAgentClient{}, cacheRoot: fixture.stateRoot}
	fixture.installMaterializingAgent(agent)

	var statuses = make(chan runtimecontract.ImageDeliveryStatus, 2)
	for range 2 {
		go func() {
			status, err := fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
			require.NoError(t, err)
			statuses <- status
		}()
	}
	for range 2 {
		require.Equal(t, runtimecontract.ImageDelivering, <-statuses)
	}
	require.Eventually(t, func() bool {
		_, resolveErr := resolveRootfsImage(fixture.stateRoot, fixture.sandboxSpec.Spec.Image)
		return resolveErr == nil
	}, 5*time.Second, 20*time.Millisecond)
	require.Equal(t, 1, agent.deliveredPins(), "concurrent deliveries must coalesce into one attempt")
}

func TestDeliverImageReportsFailureOnceThenRecoversAfterWindow(t *testing.T) {
	fixture := newDriverFixture(t)
	agent := &materializingAgent{fakeAgentClient: &fakeAgentClient{}, cacheRoot: fixture.stateRoot}
	fixture.installMaterializingAgent(agent)
	// Shorten the sticky window so the test observes failure AND the
	// window-expiry retry without waiting real minutes.
	fixture.driver.imageDeliveryFailureWindow = 300 * time.Millisecond
	boom := errors.New("remote image index 404")

	agent.setPinErr(boom)
	status, err := fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.NoError(t, err)
	require.Equal(t, runtimecontract.ImageDelivering, status)

	// The finished attempt failure is reported at most once per window.
	var reported error
	require.Eventually(t, func() bool {
		_, err := fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
		if err != nil {
			reported = err
			return true
		}
		return false
	}, 5*time.Second, 20*time.Millisecond, "the failed attempt must surface to polling callers")
	require.ErrorIs(t, reported, boom)

	// Within the window the failure stays sticky (no doomed new attempt).
	_, err = fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.ErrorIs(t, err, boom)

	// After the window a fresh attempt runs; once the remote recovers the
	// image is delivered.
	time.Sleep(400 * time.Millisecond)
	agent.setPinErr(nil)
	status, err = fixture.driver.DeliverImage(context.Background(), fixture.sandboxSpec.Spec.Image)
	require.NoError(t, err)
	require.Equal(t, runtimecontract.ImageDelivering, status)
	require.Eventually(t, func() bool {
		_, resolveErr := resolveRootfsImage(fixture.stateRoot, fixture.sandboxSpec.Spec.Image)
		return resolveErr == nil
	}, 5*time.Second, 20*time.Millisecond)
}
