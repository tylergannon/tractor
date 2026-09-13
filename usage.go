package gimble

import (
	"maps"
	"slices"
)

// Tokens is what one model read and wrote, in the five fields every adapter
// reports. A field the provider does not report is zero: zero is a number,
// and no field is ever omitted.
type Tokens struct {
	// Input is the uncached prompt: the provider's input tokens less the
	// cache read (and less the cache write where the provider folds it in).
	Input float64 `json:"input"`
	// Output is the visible output: the provider's output tokens less the
	// reasoning tokens.
	Output float64 `json:"output"`
	// Reasoning is the reasoning or thinking tokens.
	Reasoning float64 `json:"reasoning"`
	// Cache is the prompt tokens the cache served and the prompt tokens
	// written into it.
	Cache struct {
		// Read is the prompt tokens served from cache.
		Read float64 `json:"read"`
		// Write is the prompt tokens written to cache.
		Write float64 `json:"write"`
	} `json:"cache"`
}

// Usage is what one model spent: its tokens and the cost in US dollars the
// harness stated, which is zero when the harness stated none.
type Usage struct {
	Cost   float64 `json:"cost"`
	Tokens Tokens  `json:"tokens"`
}

// add sums one usage into another, field by field, as OpenCode's
// SessionUsage.add does.
func (u *Usage) add(other Usage) {
	u.Cost += other.Cost
	u.Tokens.Input += other.Tokens.Input
	u.Tokens.Output += other.Tokens.Output
	u.Tokens.Reasoning += other.Tokens.Reasoning
	u.Tokens.Cache.Read += other.Tokens.Cache.Read
	u.Tokens.Cache.Write += other.Tokens.Cache.Write
}

// ModelUsage is what one model spent, under its name. The run log carries a
// turn's usage as one entry per model: the type grammar the log's schema is
// generated from has no map shape.
type ModelUsage struct {
	Model  string  `json:"model"`
	Cost   float64 `json:"cost"`
	Tokens Tokens  `json:"tokens"`
}

// modelUsage is one turn's report as the run log carries it, ordered by
// model name so one turn always records the same way.
func modelUsage(report map[string]Usage) []ModelUsage {
	out := make([]ModelUsage, 0, len(report))
	for _, model := range slices.Sorted(maps.Keys(report)) {
		out = append(out, ModelUsage{Model: model, Cost: report[model].Cost, Tokens: report[model].Tokens})
	}
	return out
}
