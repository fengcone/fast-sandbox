package firecracker

// image_delivery.go implements the asynchronous artifact delivery of the
// driver (runtimecontract.ImageDelivery): a create whose rootfs image is not
// cached never blocks on the node-side pull. DeliverImage guarantees one
// background delivery attempt per image is in flight and reports the image as
// Delivering; the caller (Fastlet) parks the Sandbox and polls. A finished
// attempt reports its result: Delivered once the committed cache is visible,
// or the sticky attempt error so polling callers can fail the Sandbox with a
// terminal observation instead of retrying forever.
//
// Delivery attempts are single-flight per image: concurrent create intents
// for the same cold image coalesce into one PinImage (the agent journal
// dedupes replays by the pod-scoped warm-pull request id anyway). A failed
// attempt stays sticky for a short report window so polling callers observe
// the failure at least once; after the window a later call starts a fresh
// attempt, which lets a re-created Sandbox recover once the image becomes
// available.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	runtimecontract "fast-sandbox/internal/runtime/contract"

	"k8s.io/klog/v2"
)

const (
	// defaultImageDeliveryAttemptTimeout bounds one background delivery
	// attempt (image index + manifest + artifact set transfer).
	defaultImageDeliveryAttemptTimeout = 30 * time.Minute

	// defaultImageDeliveryFailureWindow keeps a failed delivery sticky for
	// this long: within the window DeliverImage reports the attempt error
	// instead of immediately starting a doomed attempt, so a polling worker
	// can count failures; after it, a new attempt is started on demand.
	defaultImageDeliveryFailureWindow = 30 * time.Second
)

// imageDelivery tracks the delivery state of one image.
type imageDelivery struct {
	mu        sync.Mutex
	inFlight  bool      // a delivery attempt is running
	failedErr error     // result of the last finished attempt, when it failed
	failedAt  time.Time // when the last attempt failed
}

// DeliverImage implements runtimecontract.ImageDelivery. A cached image
// reports Delivered without touching the agent. A missing image with no
// runtime-agent configured is an error (local mode cannot deliver); otherwise
// one background delivery attempt per image is kept in flight and the image
// reports Delivering. A recent attempt failure is reported once (sticky
// window); afterwards the next call starts a fresh attempt.
func (d *Driver) DeliverImage(_ context.Context, image string) (runtimecontract.ImageDeliveryStatus, error) {
	d.mu.RLock()
	stateRoot := d.config.StateRoot
	d.mu.RUnlock()
	if strings.TrimSpace(image) == "" {
		return "", fmt.Errorf("%w: image reference is required", ErrInvalidConfig)
	}
	if _, err := resolveRootfsImage(stateRoot, image); err == nil {
		d.touchImage(image)
		return runtimecontract.ImageDelivered, nil
	}
	client, err := d.agentClientOrNil()
	if err != nil {
		return "", err
	}
	if client == nil {
		return "", fmt.Errorf("%w: %q is not cached and no runtime-agent is configured to deliver it", ErrImageNotReady, image)
	}

	d.mu.Lock()
	entry := d.imageDeliveryLocked(image)
	d.mu.Unlock()

	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.inFlight {
		return runtimecontract.ImageDelivering, nil
	}
	if entry.failedErr != nil && time.Since(entry.failedAt) < d.deliveryFailureWindowSetting() {
		failed := entry.failedErr
		return "", failed
	}
	entry.inFlight = true
	entry.failedErr = nil
	entry.failedAt = time.Time{}
	go d.runImageDeliveryAttempt(image, entry)
	return runtimecontract.ImageDelivering, nil
}

// imageDeliveryLocked returns (creating if needed) the delivery tracker of an
// image. The caller holds d.mu.
func (d *Driver) imageDeliveryLocked(image string) *imageDelivery {
	if d.imageDeliveries == nil {
		d.imageDeliveries = make(map[string]*imageDelivery)
	}
	key := imageKey(image)
	entry := d.imageDeliveries[key]
	if entry == nil {
		entry = &imageDelivery{}
		d.imageDeliveries[key] = entry
	}
	return entry
}

// runImageDeliveryAttempt performs one PinImage pull in the background and
// records the terminal result on the tracker.
func (d *Driver) runImageDeliveryAttempt(image string, entry *imageDelivery) {
	timeout := d.deliveryAttemptTimeoutSetting()
	if timeout <= 0 {
		timeout = defaultImageDeliveryAttemptTimeout
	}
	attemptContext, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := d.pinImageForDelivery(attemptContext, image)

	entry.mu.Lock()
	entry.inFlight = false
	if err == nil {
		entry.failedErr = nil
		entry.failedAt = time.Time{}
	} else {
		entry.failedErr = err
		entry.failedAt = time.Now()
	}
	entry.mu.Unlock()

	if err == nil {
		d.touchImage(image)
		klog.InfoS("firecracker image delivery completed", "image", image)
		return
	}
	klog.ErrorS(err, "firecracker image delivery attempt failed", "image", image)
}

// pinImageForDelivery pins (and thereby pulls) an image on the runtime-agent.
// The pod-scoped warm-pull request id makes replays idempotent on the agent.
func (d *Driver) pinImageForDelivery(ctx context.Context, image string) error {
	client, err := d.agentClientOrNil()
	if err != nil {
		return err
	}
	if client == nil {
		return fmt.Errorf("%w: runtime-agent disappeared while delivering %q", ErrImageNotReady, image)
	}
	if _, err := client.PinImage(ctx, d.warmPullRequestID(image), image); err != nil {
		return err
	}
	return nil
}

func (d *Driver) deliveryAttemptTimeoutSetting() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.imageDeliveryAttemptTimeout > 0 {
		return d.imageDeliveryAttemptTimeout
	}
	return defaultImageDeliveryAttemptTimeout
}

func (d *Driver) deliveryFailureWindowSetting() time.Duration {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.imageDeliveryFailureWindow > 0 {
		return d.imageDeliveryFailureWindow
	}
	return defaultImageDeliveryFailureWindow
}
