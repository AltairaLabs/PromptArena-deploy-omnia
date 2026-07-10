package omnia

import (
	"context"
	"fmt"
	"time"
)

// Resource status phase constants used to classify reconcile progress. These
// are shared with status.go's checkResource (via phaseFailed/phaseError/
// phaseRunning/phaseActive) to avoid duplicate string literals (goconst) —
// the "Ready" phase reuses the existing conditionReady constant (status.go)
// rather than redeclaring the same value under a new name.
const (
	phaseFailed  = "Failed"
	phaseError   = "Error"
	phaseRunning = "Running"
	phaseActive  = "Active"

	// opReconcile is the DeployError.Operation value for reconcile-poll failures.
	opReconcile = "reconcile"

	// reconcileDefaultMaxAttempts is the default reconcileMaxAttempts value —
	// named so it isn't a bare magic number in the var declaration below.
	// ~60s at the default 2s poll interval.
	reconcileDefaultMaxAttempts = 30
)

// Reconcile-poll tuning. Vars (not consts) so tests can zero the interval and
// cap the attempts for fast, deterministic runs.
var (
	reconcilePollInterval = 2 * time.Second
	reconcileMaxAttempts  = reconcileDefaultMaxAttempts
)

// Terminal-failure phases: the resource reconciled into a bad state, so polling
// further is pointless — fail loudly and immediately.
var reconcileFailedPhases = map[string]bool{
	phaseFailed: true,
	phaseError:  true,
}

// Healthy phases that mean "reconciled" even without an explicit Ready condition.
var reconcileReadyPhases = map[string]bool{
	phaseRunning:   true,
	phaseActive:    true,
	conditionReady: true,
}

// waitForReconcile polls the resource until it reports Ready, then returns nil.
// It returns a *DeployError if the resource enters a terminal-failed phase, or if
// it never becomes Ready within reconcileMaxAttempts polls (the "created (201) but
// silently never reconciled" case — e.g. a CRD-schema mismatch the operator drops).
// Unlike checkResource, an empty/unknown status is treated as NOT ready (keep
// waiting), never as healthy.
func waitForReconcile(ctx context.Context, client omniaClient, resType, name string) error {
	for attempt := 0; attempt < reconcileMaxAttempts; attempt++ {
		state, err := reconcileState(ctx, client, resType, name)
		if err != nil {
			return err
		}
		switch state {
		case reconcileDone:
			return nil
		case reconcileFailed:
			return newReconcileError(resType, name,
				fmt.Errorf("resource reconciled into a failed state"),
				"check the operator logs and the resource's status.conditions")
		case reconcilePending:
			// keep polling
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(reconcilePollInterval):
		}
	}
	return newReconcileTimeoutError(resType, name)
}

type reconcilePhase int

const (
	reconcilePending reconcilePhase = iota
	reconcileDone
	reconcileFailed
)

// reconcileState fetches the resource and classifies its readiness. A 404/transport
// error is surfaced (the caller fails the deploy).
func reconcileState(ctx context.Context, client omniaClient, resType, name string) (reconcilePhase, error) {
	resp, err := client.GetResource(ctx, resType, name)
	if err != nil {
		return reconcilePending, newReconcileError(resType, name, err,
			"the resource could not be read back after creation")
	}
	if resp.Status == nil {
		return reconcilePending, nil
	}
	for _, cond := range resp.Status.Conditions {
		if cond.Type == conditionReady && cond.Status == conditionTrue {
			return reconcileDone, nil
		}
	}
	if reconcileFailedPhases[resp.Status.Phase] {
		return reconcileFailed, nil
	}
	if reconcileReadyPhases[resp.Status.Phase] {
		return reconcileDone, nil
	}
	return reconcilePending, nil
}

// newReconcileError builds a resource-category DeployError for a reconcile failure.
func newReconcileError(resType, name string, cause error, remediation string) *DeployError {
	return &DeployError{
		Category:     ErrCategoryResource,
		ResourceType: resType,
		ResourceName: name,
		Operation:    opReconcile,
		Message:      cause.Error(),
		Remediation:  remediation,
		Cause:        cause,
	}
}

// newReconcileTimeoutError builds the "created but never reconciled" timeout error.
func newReconcileTimeoutError(resType, name string) *DeployError {
	return &DeployError{
		Category:     ErrCategoryTimeout,
		ResourceType: resType,
		ResourceName: name,
		Operation:    opReconcile,
		Message: fmt.Sprintf("created but did not reach Ready within %d polls",
			reconcileMaxAttempts),
		Remediation: "the resource was created but the operator never reconciled it — verify " +
			"the operator is running and the CRD schema version matches the adapter's",
	}
}
