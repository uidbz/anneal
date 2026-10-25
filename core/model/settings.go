package model

import (
	"strings"

	"github.com/uidbz/anneal/core/config"

	"github.com/deepnoodle-ai/dive"
	"github.com/deepnoodle-ai/dive/llm"
)

// Settings maps the provider section of anneal's config onto dive model
// settings. Centralized so the main runner, probes, and any future agent
// construct the exact same request options (and so reasoning_effort actually
// reaches the provider).
func Settings(cfg *config.Config) *dive.ModelSettings {
	if cfg == nil {
		return nil
	}
	ms := &dive.ModelSettings{
		MaxTokens:   dive.Ptr(cfg.Provider.MaxTokens),
		Temperature: dive.Ptr(cfg.Provider.Temperature),
	}
	if effort := strings.TrimSpace(cfg.Provider.ReasoningEffort); effort != "" {
		ms.ReasoningEffort = llm.ReasoningEffort(effort)
	}
	return ms
}
