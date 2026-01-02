package scheduler

import (
	"context"
	"errors"

	apiv1alpha1 "fast-sandbox/api/v1alpha1"
	"fast-sandbox/internal/controller/agentpool"
)

// Scheduler defines the interface for selecting an agent for a SandboxClaim.
type Scheduler interface {
	Schedule(ctx context.Context, claim *apiv1alpha1.SandboxClaim, agents []agentpool.AgentInfo) (agentpool.AgentInfo, error)
}

// SimpleScheduler is a basic implementation using image affinity and free capacity.
type SimpleScheduler struct{}

// NewSimpleScheduler creates a SimpleScheduler.
func NewSimpleScheduler() *SimpleScheduler {
	return &SimpleScheduler{}
}

// Schedule selects an agent with the requested image and highest free capacity.
func (s *SimpleScheduler) Schedule(ctx context.Context, claim *apiv1alpha1.SandboxClaim, agents []agentpool.AgentInfo) (agentpool.AgentInfo, error) {
	var best agentpool.AgentInfo
	bestScore := -1

	image := claim.Spec.Image
	for _, a := range agents {
		free := a.Capacity - a.Allocated
		if free <= 0 {
			continue
		}

		hasImage := false
		for _, img := range a.Images {
			if img == image {
				hasImage = true
				break
			}
		}

		score := free
		if hasImage {
			score += 1000
		}

		if score > bestScore {
			bestScore = score
			best = a
		}
	}

	if bestScore < 0 {
		return agentpool.AgentInfo{}, errors.New("no available agent")
	}

	return best, nil
}
