package llm

import "testing"

func TestProvider(t *testing.T) {
	cases := map[string]string{
		"claude-opus-4-8":   providerAnthropic,
		"claude-sonnet-4-5": providerAnthropic,
		"gemini-2.5-pro":    providerGemini,
		"gemini-2.5-flash":  providerGemini,
		"Gemini-1.5-Pro":    providerGemini,    // case-insensitive
		"something-else":    providerAnthropic, // default
	}
	for name, want := range cases {
		if got := Provider(name); got != want {
			t.Errorf("Provider(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestConfiguredModel(t *testing.T) {
	t.Setenv("AGENTBOX_MODEL", "")
	if got := ConfiguredModel(); got != DefaultModel {
		t.Errorf("default model = %q, want %q", got, DefaultModel)
	}
	t.Setenv("AGENTBOX_MODEL", "gemini-2.5-pro")
	if got := ConfiguredModel(); got != "gemini-2.5-pro" {
		t.Errorf("override model = %q", got)
	}
}

func TestRequireKey(t *testing.T) {
	clearKeys := func(t *testing.T) {
		for _, k := range []string{"ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_GENAI_USE_VERTEXAI"} {
			t.Setenv(k, "")
		}
	}

	t.Run("claude without key", func(t *testing.T) {
		clearKeys(t)
		if err := RequireKey("claude-opus-4-8"); err == nil {
			t.Error("expected error when ANTHROPIC_API_KEY is missing")
		}
	})
	t.Run("claude with key", func(t *testing.T) {
		clearKeys(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
		if err := RequireKey("claude-opus-4-8"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("gemini without key", func(t *testing.T) {
		clearKeys(t)
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x") // wrong key for gemini
		if err := RequireKey("gemini-2.5-pro"); err == nil {
			t.Error("expected error when no Google key is set")
		}
	})
	t.Run("gemini with GEMINI_API_KEY", func(t *testing.T) {
		clearKeys(t)
		t.Setenv("GEMINI_API_KEY", "g-x")
		if err := RequireKey("gemini-2.5-pro"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("gemini with GOOGLE_API_KEY", func(t *testing.T) {
		clearKeys(t)
		t.Setenv("GOOGLE_API_KEY", "g-x")
		if err := RequireKey("gemini-2.5-pro"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("gemini via vertex flag", func(t *testing.T) {
		clearKeys(t)
		t.Setenv("GOOGLE_GENAI_USE_VERTEXAI", "true")
		if err := RequireKey("gemini-2.5-pro"); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
}
