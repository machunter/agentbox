// Package llm selects and builds the language model behind the agent. It
// supports multiple providers behind ADK's model.LLM interface: Claude (via the
// Anthropic adapter) and Gemini (native to ADK). The model is chosen by name
// (AGENTBOX_MODEL); the provider is inferred from the name, and the matching
// API key is read from the environment.
package llm

import (
	"context"
	"fmt"
	"os"
	"strings"

	adkanthropic "github.com/Alcova-AI/adk-anthropic-go"
	"github.com/anthropics/anthropic-sdk-go"
	"google.golang.org/adk/model"
	"google.golang.org/adk/model/gemini"
	"google.golang.org/genai"
)

// DefaultModel is used when AGENTBOX_MODEL is unset.
const DefaultModel = "claude-opus-4-8"

const (
	providerAnthropic = "anthropic"
	providerGemini    = "gemini"
)

// ConfiguredModel returns the model name from AGENTBOX_MODEL, or the default.
func ConfiguredModel() string {
	if m := os.Getenv("AGENTBOX_MODEL"); m != "" {
		return m
	}
	return DefaultModel
}

// Provider infers the provider from a model name. Names starting with "gemini"
// use Gemini; everything else (claude-*) uses Anthropic.
func Provider(modelName string) string {
	if strings.HasPrefix(strings.ToLower(modelName), "gemini") {
		return providerGemini
	}
	return providerAnthropic
}

// RequireKey reports whether the API key needed for modelName's provider is
// present, returning a clear error if not. Use it for an early preflight before
// starting work, so failures don't surface only mid-run.
func RequireKey(modelName string) error {
	switch Provider(modelName) {
	case providerGemini:
		if !hasGeminiAuth() {
			return fmt.Errorf("model %q needs a Google API key (set GEMINI_API_KEY or GOOGLE_API_KEY)", modelName)
		}
	default:
		if os.Getenv("ANTHROPIC_API_KEY") == "" {
			return fmt.Errorf("model %q needs ANTHROPIC_API_KEY", modelName)
		}
	}
	return nil
}

// New builds the ADK model.LLM for modelName, selecting the provider and reading
// its API key from the environment.
func New(ctx context.Context, modelName string) (model.LLM, error) {
	if err := RequireKey(modelName); err != nil {
		return nil, err
	}
	switch Provider(modelName) {
	case providerGemini:
		// genai.NewClient reads GEMINI_API_KEY / GOOGLE_API_KEY (and the Vertex
		// flag) from the environment, so an empty config is enough.
		m, err := gemini.NewModel(ctx, modelName, &genai.ClientConfig{})
		if err != nil {
			return nil, fmt.Errorf("init gemini model: %w", err)
		}
		return m, nil
	default:
		m, err := adkanthropic.NewModel(ctx, anthropic.Model(modelName), &adkanthropic.Config{
			APIKey: os.Getenv("ANTHROPIC_API_KEY"),
		})
		if err != nil {
			return nil, fmt.Errorf("init anthropic model: %w", err)
		}
		return m, nil
	}
}

func hasGeminiAuth() bool {
	return os.Getenv("GEMINI_API_KEY") != "" ||
		os.Getenv("GOOGLE_API_KEY") != "" ||
		isTrue(os.Getenv("GOOGLE_GENAI_USE_VERTEXAI"))
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes":
		return true
	}
	return false
}
