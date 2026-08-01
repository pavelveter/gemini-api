package main

import (
	"fmt"
	"strconv"
	"strings"
)

// ModelConfig maps a model name to Gemini frontend mode settings.
// MODE_CATEGORY enum from the Gemini frontend JS source:
//
//	1=FAST, 2=THINKING, 3=PRO, 4=AUTO, 5=FAST_DYNAMIC_THINKING, 6=FLASH_LITE
type ModelConfig struct {
	Mode  int
	Think int
	Desc  string
	Extra map[int]any
}

// modelOrder preserves the insertion order of the original Python dict.
var modelOrder = []string{
	"gemini-3.6-flash",
	"gemini-3.5-flash",
	"gemini-3.5-flash-thinking",
	"gemini-3.1-pro",
	"gemini-3.1-pro-enhanced",
	"gemini-auto",
	"gemini-3.5-flash-thinking-lite",
	"gemini-flash-lite",
}

// MODELS is the model table.
var MODELS = map[string]ModelConfig{
	"gemini-3.6-flash":               {Mode: 1, Think: 4, Desc: "Latest all-around model (Gemini 3.6 Flash)"},
	"gemini-3.5-flash":               {Mode: 1, Think: 4, Desc: "Alias for gemini-3.6-flash (backend upgraded)"},
	"gemini-3.5-flash-thinking":      {Mode: 2, Think: 0, Desc: "Deep thinking mode, longest output (~20k chars)"},
	"gemini-3.1-pro":                 {Mode: 3, Think: 4, Desc: "Pro model (requires cookie for real routing)"},
	"gemini-3.1-pro-enhanced":        {Mode: 3, Think: 4, Extra: map[int]any{31: 2, 80: 3}, Desc: "Pro with enhanced output (experimental)"},
	"gemini-auto":                    {Mode: 4, Think: 4, Desc: "Auto model selection"},
	"gemini-3.5-flash-thinking-lite": {Mode: 5, Think: 0, Desc: "Dynamic thinking with adaptive depth"},
	"gemini-flash-lite":              {Mode: 6, Think: 4, Desc: "Lightweight fast model"},
}

func modelNames() []string {
	return modelOrder
}

// resolvedModel is the result of resolving a requested model name.
type resolvedModel struct {
	Name      string
	ModeID    int
	ThinkMode int
	Extra     map[int]any
}

// resolveModel resolves a model name to its mode/think settings, supporting
// the "@think=N" suffix. Unknown model names fall back to the default model
// rather than erroring, since upstream clients may request arbitrary names.
func resolveModel(modelName, defaultModel string) (resolvedModel, error) {
	thinkOverride := -1
	if i := strings.Index(modelName, "@think="); i >= 0 {
		thinkStr := modelName[i+len("@think="):]
		modelName = modelName[:i]
		n, err := strconv.Atoi(thinkStr)
		if err != nil {
			return resolvedModel{}, fmt.Errorf("invalid think level: %s", thinkStr)
		}
		thinkOverride = n
	}
	cfg, ok := MODELS[modelName]
	if !ok {
		log(fmt.Sprintf("Unknown model '%s', falling back to '%s'", modelName, defaultModel))
		modelName = defaultModel
		cfg = MODELS[defaultModel]
	}
	rm := resolvedModel{Name: modelName, ModeID: cfg.Mode, ThinkMode: cfg.Think, Extra: cfg.Extra}
	if thinkOverride >= 0 {
		rm.ThinkMode = thinkOverride
	}
	return rm, nil
}
