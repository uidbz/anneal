// Package model builds the dive LLM provider for Strata (OpenAI Chat
// Completions) and monitors the endpoint's health/model list.
package model

import (
	"fyagent/core/config"

	"github.com/deepnoodle-ai/dive/providers/openaicompletions"
)

// NewProvider builds a dive provider for Strata. modelID is the concrete
// model to send (resolve "auto" via the HealthMonitor first).
func NewProvider(p config.Provider, modelID string) *openaicompletions.Provider {
	firstEvent, idle := p.StreamTimeouts()
	if modelID == "" || modelID == "auto" {
		modelID = "qwen3.8-flash-next"
	}
	return openaicompletions.New(
		openaicompletions.WithName("strata"),
		openaicompletions.WithEndpoint(p.Endpoint),
		openaicompletions.WithAPIKey(p.APIKey),
		openaicompletions.WithModel(modelID),
		openaicompletions.WithMaxRetries(p.MaxRetries),
		openaicompletions.WithMaxTokens(p.MaxTokens),
		openaicompletions.WithStreamTimeouts(firstEvent, idle),
		// Local endpoint: never attribute a cloud price catalog to it.
		openaicompletions.WithDisableCatalogCost(),
	)
}
