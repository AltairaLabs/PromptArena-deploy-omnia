package omnia

// Label key constants applied to all Omnia resources.
const (
	LabelManagedBy = "app.kubernetes.io/managed-by"
	LabelPackID    = "promptkit.altairalabs.ai/pack-id"
	LabelPackVer   = "promptkit.altairalabs.ai/pack-version"
	LabelResType   = "promptkit.altairalabs.ai/resource-type"
)

// managedByValue is the value for the managed-by label.
const managedByValue = "promptarena"

// buildResourceLabels creates the label map for a resource, merging
// pack metadata labels with user-supplied extra labels. User labels
// cannot override the managed-by or pack metadata labels.
func buildResourceLabels(packID, packVersion, resType string, extra map[string]string) map[string]string {
	labels := make(map[string]string, len(extra)+4) //nolint:mnd // 4 base labels
	for k, v := range extra {
		labels[k] = v
	}
	// Managed labels always win.
	labels[LabelManagedBy] = managedByValue
	labels[LabelPackID] = packID
	labels[LabelPackVer] = packVersion
	labels[LabelResType] = resType
	return labels
}
