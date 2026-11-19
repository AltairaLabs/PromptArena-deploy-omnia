package omnia

import "encoding/json"

// partialApplyState builds the adapter state a failed apply returns. The CLI
// saves it in place of the stored state (the adapter advertises
// deploy.PartialApplyStateCapability), so it must describe everything that
// exists after the failure, not only what this apply touched: it is the prior
// state with this apply's results merged in (see mergePartialResources).
//
// existing is what the apply knew to exist before writing: the resources it
// adopted from the cluster, plus any whose create answered AlreadyExists.
//
// When the merge adds nothing to the prior state, priorState is returned
// verbatim. The CLI recognizes an unchanged prior state and keeps what it has
// stored.
func partialApplyState(
	priorState string, existing map[string]ResourceState, touched []ResourceState, packID, version string,
) (string, error) {
	var prior AdapterState
	if priorState != "" {
		if err := json.Unmarshal([]byte(priorState), &prior); err != nil {
			// A state this adapter cannot read cannot be merged into, and
			// replacing it would forget everything it records. Returning it
			// unchanged makes the CLI keep it; plan and apply adopt from the
			// cluster, so what this apply wrote is found again.
			return priorState, nil
		}
	}
	merged, changed := mergePartialResources(prior.Resources, existing, touched)
	if !changed {
		return priorState, nil
	}
	stateJSON, err := json.Marshal(AdapterState{Resources: merged, PackID: packID, Version: version})
	if err != nil {
		return "", err
	}
	return string(stateJSON), nil
}

// mergePartialResources merges the resources a failed apply touched into the
// prior state's resources, keeping the prior order and appending new ones:
//
//   - a resource the apply created or updated replaces its prior entry;
//   - a resource it left unchanged keeps its prior entry, which carries the
//     UID and resourceVersion an unchanged result does not, or is added;
//   - a resource whose write failed keeps its prior entry. Without one it is
//     added as unchanged when existing says it is there, and left out
//     otherwise, because its create never happened;
//   - a prior resource the apply never reached is kept as it was, unless its
//     entry never described a real resource (see priorEntryExists).
//
// changed reports whether the result differs from prior.
func mergePartialResources(
	prior []ResourceState, existing map[string]ResourceState, touched []ResourceState,
) (merged []ResourceState, changed bool) {
	index := make(map[string]int, len(prior)+len(touched))
	merged = make([]ResourceState, 0, len(prior)+len(touched))
	for _, r := range prior {
		key := resourceKey(r.Type, r.Name)
		if _, dup := index[key]; dup || !priorEntryExists(r.Status) {
			continue
		}
		index[key] = len(merged)
		merged = append(merged, r)
	}
	for _, r := range touched {
		key := resourceKey(r.Type, r.Name)
		i, known := index[key]
		switch {
		case r.Status == ResStatusCreated || r.Status == ResStatusUpdated:
			changed = true
			if known {
				merged[i] = r
				continue
			}
		case known:
			continue
		case r.Status == ResStatusFailed:
			if _, exists := existing[key]; !exists {
				continue
			}
			r = ResourceState{Type: r.Type, Name: r.Name, Status: ResStatusUnchanged}
		}
		changed = true
		index[key] = len(merged)
		merged = append(merged, r)
	}
	return merged, changed
}

// priorEntryExists reports whether a prior-state entry describes a resource
// that was really written. A dry-run apply's state records planned entries,
// and an older adapter's state could record failed ones; neither exists.
func priorEntryExists(status string) bool {
	return status != ResStatusPlanned && status != ResStatusFailed
}
