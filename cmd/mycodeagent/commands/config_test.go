package commands

import "testing"

// The global `model` key is the user's setting. `mycodeagent config` may claim it
// only when it is unset or already points at a provider this command owns.
//
// The regression these lock in: opencode knows subscription providers
// (opencode-go, openrouter, …) natively, so they never appear in the config's
// `provider` map. Deciding "may I keep the user's default?" by looking that key
// up in the map therefore answered "no" for every subscription model, and the
// default was silently rewritten to a local one on every run.
func TestChooseDefaultModel(t *testing.T) {
	running := map[string]any{"mycodeagent-coder-9": struct{}{}}

	for _, tc := range []struct {
		name      string
		existing  string
		candidate string
		providers map[string]any
		want      string
		wantKeep  bool
	}{
		{
			name:      "subscription default survives a deploy",
			existing:  "opencode-go/kimi-k2.6",
			candidate: "mycodeagent-coder-9/qwen36-27b-24g",
			providers: running,
			want:      "opencode-go/kimi-k2.6",
			wantKeep:  true,
		},
		{
			name:      "other third-party provider survives too",
			existing:  "openrouter/some-model",
			candidate: "mycodeagent-coder-9/qwen36-27b-24g",
			providers: running,
			want:      "openrouter/some-model",
			wantKeep:  true,
		},
		{
			name:      "a locally configured provider is still the user's",
			existing:  "lmstudio/qwen3.5-9b",
			candidate: "mycodeagent-coder-9/qwen36-27b-24g",
			providers: running,
			want:      "lmstudio/qwen3.5-9b",
			wantKeep:  true,
		},
		{
			name:      "unset default adopts the new instance",
			existing:  "",
			candidate: "mycodeagent-coder-9/qwen36-27b-24g",
			providers: running,
			want:      "mycodeagent-coder-9/qwen36-27b-24g",
			wantKeep:  true,
		},
		{
			name:      "our own default is kept while its instance runs",
			existing:  "mycodeagent-coder-9/qwen36-27b-24g",
			candidate: "mycodeagent-coder-9/qwen36-27b-24g",
			providers: running,
			want:      "mycodeagent-coder-9/qwen36-27b-24g",
			wantKeep:  true,
		},
		{
			name:      "a dead instance of ours is replaced by the live one",
			existing:  "mycodeagent-74/old-model",
			candidate: "mycodeagent-coder-9/qwen36-27b-24g",
			providers: running,
			want:      "mycodeagent-coder-9/qwen36-27b-24g",
			wantKeep:  true,
		},
		{
			name:      "a dead instance with no replacement clears the key",
			existing:  "mycodeagent-74/old-model",
			candidate: "",
			providers: map[string]any{},
			want:      "",
			wantKeep:  false,
		},
		{
			name:      "nothing set and nothing running leaves no key",
			existing:  "",
			candidate: "",
			providers: map[string]any{},
			want:      "",
			wantKeep:  false,
		},
		{
			name:      "subscription default survives with nothing running",
			existing:  "opencode-go/kimi-k2.6",
			candidate: "",
			providers: map[string]any{},
			want:      "opencode-go/kimi-k2.6",
			wantKeep:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, keep := chooseDefaultModel(tc.existing, tc.candidate, tc.providers)
			if got != tc.want || keep != tc.wantKeep {
				t.Errorf("chooseDefaultModel(%q, %q) = (%q, %v), want (%q, %v)",
					tc.existing, tc.candidate, got, keep, tc.want, tc.wantKeep)
			}
		})
	}
}

// The temperature must be scoped to our own model entries. It used to be written
// as a global `mode` block, which re-tuned the user's subscription models too.
func TestBuildModelConfigScopesTemperature(t *testing.T) {
	m := buildModelConfig("qwen36-27b-24g", 65536)

	opts, ok := m["options"].(map[string]any)
	if !ok {
		t.Fatalf("model entry has no options block: %+v", m)
	}
	if opts["temperature"] != 0.6 {
		t.Errorf("temperature = %v, want 0.6", opts["temperature"])
	}
	limit, ok := m["limit"].(map[string]any)
	if !ok || limit["context"] != 65536 {
		t.Errorf("served context not recorded: %+v", m["limit"])
	}

	// A server that didn't report a window must not get a bogus limit.
	if _, has := buildModelConfig("x", 0)["limit"]; has {
		t.Error("limit written despite an unknown context length")
	}
}
