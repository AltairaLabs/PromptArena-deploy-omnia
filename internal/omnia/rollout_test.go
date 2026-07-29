package omnia

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestIntentRollout(t *testing.T) {
	got := intentRollout(&RolloutConfig{
		Channel: "stable",
		Steps: []RolloutStep{
			{SetWeight: intPtr(25)},
			{Pause: "30s"},
			{SetWeight: intPtr(100)},
		},
	})
	if got == nil {
		t.Fatal("want a rollout block")
	}
	if got.Trigger == nil || got.Trigger.PromptPackChannel != "stable" {
		t.Errorf("trigger = %+v, want the stable channel", got.Trigger)
	}
	if len(got.Steps) != 3 {
		t.Fatalf("steps = %+v, want 3", got.Steps)
	}
	if got.Steps[0].SetWeight == nil || *got.Steps[0].SetWeight != 25 {
		t.Errorf("steps[0] = %+v, want setWeight 25", got.Steps[0])
	}
	if got.Steps[0].PauseDuration != "" {
		t.Errorf("steps[0] invented a pause: %+v", got.Steps[0])
	}
	if got.Steps[1].PauseDuration != "30s" || got.Steps[1].SetWeight != nil {
		t.Errorf("steps[1] = %+v, want a bare 30s pause", got.Steps[1])
	}
}

func TestIntentRollout_NilWhenUnconfigured(t *testing.T) {
	// This is what keeps a controller-driven rollout intact: the server preserves
	// the live rollout wholesale precisely because the intent omits the block.
	if got := intentRollout(nil); got != nil {
		t.Errorf("rollout = %+v, want nil when no policy is configured", got)
	}
	// A block with no channel is not a policy either — validate() rejects it, and
	// sending a half-formed rollout would clobber a live one.
	if got := intentRollout(&RolloutConfig{Steps: []RolloutStep{{SetWeight: intPtr(10)}}}); got != nil {
		t.Errorf("rollout = %+v, want nil when no channel is set", got)
	}
}

func TestIntentRollout_CarriesNoLiveState(t *testing.T) {
	// The contract type must not grow candidate/stickySession/rollback. Those are
	// controller-owned, and sending them would cancel an in-flight canary.
	got := intentRollout(&RolloutConfig{Channel: "stable", Steps: []RolloutStep{{SetWeight: intPtr(50)}}})
	rendered := jsonOfRollout(t, got)
	for _, forbidden := range []string{"candidate", "stickySession", "rollback", "trafficRouting"} {
		if strings.Contains(rendered, forbidden) {
			t.Errorf("rollout intent carries %q — that is controller-owned live state: %s",
				forbidden, rendered)
		}
	}
}

func TestValidateRollout(t *testing.T) {
	tests := []struct {
		name string
		cfg  *RolloutConfig
		want string // substring the error must contain; "" means valid
	}{
		{name: "not configured", cfg: nil},
		{
			name: "valid",
			cfg:  &RolloutConfig{Channel: "stable", Steps: []RolloutStep{{SetWeight: intPtr(25)}}},
		},
		{
			name: "valid prerelease with pause",
			cfg:  &RolloutConfig{Channel: "prerelease", Steps: []RolloutStep{{Pause: "1m"}}},
		},
		{
			name: "missing channel",
			cfg:  &RolloutConfig{Steps: []RolloutStep{{SetWeight: intPtr(25)}}},
			want: "rollout.channel is required",
		},
		{
			name: "unknown channel",
			cfg:  &RolloutConfig{Channel: "beta", Steps: []RolloutStep{{SetWeight: intPtr(25)}}},
			want: `rollout.channel "beta" is invalid`,
		},
		{
			// The CRD sets MinItems=1, so this would be rejected by the apiserver
			// mid-apply rather than caught at plan time.
			name: "no steps",
			cfg:  &RolloutConfig{Channel: "stable"},
			want: "rollout.steps must not be empty",
		},
		{
			name: "empty step",
			cfg:  &RolloutConfig{Channel: "stable", Steps: []RolloutStep{{}}},
			want: "rollout.steps[0] is empty",
		},
		{
			name: "step doing both",
			cfg: &RolloutConfig{Channel: "stable", Steps: []RolloutStep{
				{SetWeight: intPtr(25), Pause: "30s"},
			}},
			want: "sets both setWeight and pause",
		},
		{
			name: "weight over 100",
			cfg:  &RolloutConfig{Channel: "stable", Steps: []RolloutStep{{SetWeight: intPtr(150)}}},
			want: "it is a percentage",
		},
		{
			name: "negative weight",
			cfg:  &RolloutConfig{Channel: "stable", Steps: []RolloutStep{{SetWeight: intPtr(-1)}}},
			want: "it is a percentage",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := validateRollout(tt.cfg)
			if tt.want == "" {
				if len(errs) != 0 {
					t.Fatalf("want no errors, got %v", errs)
				}
				return
			}
			joined := strings.Join(errs, "; ")
			if !strings.Contains(joined, tt.want) {
				t.Errorf("errors = %v, want one containing %q", errs, tt.want)
			}
		})
	}
}

func TestConfigValidate_SurfacesRolloutErrors(t *testing.T) {
	cfg, err := parseConfig(`{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "t",
		"providers": {"default": "claude-prod"},
		"rollout": {"channel": "nightly", "steps": []}
	}`)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	joined := strings.Join(cfg.validate(), "; ")
	if !strings.Contains(joined, "rollout.channel") {
		t.Errorf("validate() = %q, want the bad channel reported", joined)
	}
	if !strings.Contains(joined, "rollout.steps must not be empty") {
		t.Errorf("validate() = %q, want the empty steps reported", joined)
	}
}

func TestBuildDeployIntent_CarriesRollout(t *testing.T) {
	cfg := `{
		"api_endpoint": "https://omnia.test.com",
		"workspace": "test-ws",
		"api_token": "test-token",
		"providers": {"default": "claude-prod"},
		"rollout": {
			"channel": "stable",
			"steps": [{"setWeight": 25}, {"pause": "30s"}]
		}
	}`
	intent := buildIntentForTest(t, testPackJSON, cfg)

	if len(intent.Agents) == 0 {
		t.Fatal("no agents in the intent")
	}
	r := intent.Agents[0].Rollout
	if r == nil || r.Trigger == nil {
		t.Fatalf("agent rollout = %+v, want a trigger", r)
	}
	if r.Trigger.PromptPackChannel != "stable" {
		t.Errorf("channel = %q, want stable", r.Trigger.PromptPackChannel)
	}
	if len(r.Steps) != 2 {
		t.Errorf("steps = %+v, want 2", r.Steps)
	}
}

func TestBuildDeployIntent_OmitsRolloutWhenUnconfigured(t *testing.T) {
	intent := buildIntentForTest(t, testPackJSON, testDeployConfig)
	if len(intent.Agents) == 0 {
		t.Fatal("no agents in the intent")
	}
	if r := intent.Agents[0].Rollout; r != nil {
		t.Errorf("rollout = %+v, want nil so an existing one is preserved", r)
	}
}

// jsonOfRollout renders the rollout intent as JSON, for asserting on what is
// and is not on the wire.
func jsonOfRollout(t *testing.T, r *rolloutIntent) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal rollout: %v", err)
	}
	return string(b)
}
