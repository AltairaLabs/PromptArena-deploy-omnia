package omnia

import "testing"

func TestBuildResourceLabels(t *testing.T) {
	t.Run("managed labels are set", func(t *testing.T) {
		labels := buildResourceLabels("pack-1", "v1.0.0", "configmap", nil)

		checks := map[string]string{
			LabelManagedBy: managedByValue,
			LabelPackID:    "pack-1",
			LabelPackVer:   "v1.0.0",
			LabelResType:   "configmap",
		}
		for k, want := range checks {
			if got := labels[k]; got != want {
				t.Errorf("labels[%q] = %q, want %q", k, got, want)
			}
		}
	})

	t.Run("extra labels are included", func(t *testing.T) {
		extra := map[string]string{"env": "prod", "team": "platform"}
		labels := buildResourceLabels("pack-1", "v1.0.0", "configmap", extra)

		if labels["env"] != "prod" {
			t.Errorf("labels[env] = %q, want %q", labels["env"], "prod")
		}
		if labels["team"] != "platform" {
			t.Errorf("labels[team] = %q, want %q", labels["team"], "platform")
		}
	})

	t.Run("managed labels override extras", func(t *testing.T) {
		extra := map[string]string{
			LabelManagedBy: "should-be-overridden",
			LabelPackID:    "should-be-overridden",
		}
		labels := buildResourceLabels("real-pack", "v2.0.0", "prompt_pack", extra)

		if labels[LabelManagedBy] != managedByValue {
			t.Errorf("labels[%q] = %q, want %q (managed label should override extra)",
				LabelManagedBy, labels[LabelManagedBy], managedByValue)
		}
		if labels[LabelPackID] != "real-pack" {
			t.Errorf("labels[%q] = %q, want %q (managed label should override extra)",
				LabelPackID, labels[LabelPackID], "real-pack")
		}
	})
}
