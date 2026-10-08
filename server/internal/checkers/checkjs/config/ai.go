package config

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// RepairMode says what happens when an AI-authored script drifts
// (spec 2026-10-03-07).
type RepairMode string

// Repair modes.
const (
	// RepairOff never attempts a repair.
	RepairOff RepairMode = "off"
	// RepairPropose stores a repair as a proposed check version (the default).
	RepairPropose RepairMode = "propose"
	// RepairAuto applies a repair that passed every guard and a verification
	// run, and falls back to a proposal otherwise.
	RepairAuto RepairMode = "auto"
)

const (
	aiKeyPrompt      = "prompt"
	aiKeyContract    = "contract"
	aiKeyModel       = "model"
	aiKeyGeneratedAt = "generated_at"
	aiKeyRepair      = "repair"

	maxPromptLength    = 4000
	maxContractEntries = 30
	maxContractLength  = 500
)

// RepairModes lists the accepted repair values.
func RepairModes() []RepairMode {
	return []RepairMode{RepairOff, RepairPropose, RepairAuto}
}

// AIConfig is the `ai` block of an AI-authored js check.
type AIConfig struct {
	// Prompt is the user's description of what to watch.
	Prompt string `json:"prompt,omitempty"`
	// Contract is the ordered list of assertions the user confirmed.
	Contract []string `json:"contract,omitempty"`
	// Model is the model that wrote the current script.
	Model string `json:"model,omitempty"`
	// GeneratedAt is when the current script was written.
	GeneratedAt *time.Time `json:"generated_at,omitempty"` //nolint:tagliatelle // snake_case like the other config keys
	// Repair is off, propose (default) or auto.
	Repair RepairMode `json:"repair,omitempty"`
}

// EffectiveRepair is the repair mode with the default applied.
func (a *AIConfig) EffectiveRepair() RepairMode {
	if a == nil {
		return RepairOff
	}

	if a.Repair == "" {
		return RepairPropose
	}

	return a.Repair
}

func aiFieldError(key, msg string) error {
	return checkerdef.NewConfigError(fieldAI+"."+key, msg)
}

// aiFromConfig parses the optional `ai` block.
func aiFromConfig(configMap map[string]any) (*AIConfig, error) {
	raw, present := configMap[fieldAI]
	if !present || raw == nil {
		return nil, nil //nolint:nilnil // an absent block is not an error
	}

	if typed, ok := raw.(*AIConfig); ok {
		return typed, nil
	}

	block, ok := raw.(map[string]any)
	if !ok {
		return nil, checkerdef.NewConfigError(fieldAI, "must be an object")
	}

	out := &AIConfig{}

	for key, value := range block {
		if err := out.setField(key, value); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func (a *AIConfig) setField(key string, value any) error {
	if value == nil {
		return nil
	}

	switch key {
	case aiKeyPrompt, aiKeyModel, aiKeyRepair, aiKeyGeneratedAt:
		str, ok := value.(string)
		if !ok {
			return aiFieldError(key, "must be a string")
		}

		return a.setString(key, str)
	case aiKeyContract:
		contract, err := contractFromAny(value)
		if err != nil {
			return err
		}

		a.Contract = contract
	default:
		return aiFieldError(key, "is not a known field")
	}

	return nil
}

func (a *AIConfig) setString(key, value string) error {
	switch key {
	case aiKeyPrompt:
		a.Prompt = value
	case aiKeyModel:
		a.Model = value
	case aiKeyRepair:
		a.Repair = RepairMode(value)
	case aiKeyGeneratedAt:
		if value == "" {
			return nil
		}

		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return aiFieldError(key, "must be an RFC 3339 timestamp")
		}

		a.GeneratedAt = &parsed
	}

	return nil
}

func contractFromAny(value any) ([]string, error) {
	if typed, ok := value.([]string); ok {
		return typed, nil
	}

	list, ok := value.([]any)
	if !ok {
		return nil, aiFieldError(aiKeyContract, "must be a list of strings")
	}

	out := make([]string, 0, len(list))

	for _, item := range list {
		str, ok := item.(string)
		if !ok {
			return nil, aiFieldError(aiKeyContract, "must be a list of strings")
		}

		out = append(out, str)
	}

	return out, nil
}

// toMap renders the block for GetConfig.
func (a *AIConfig) toMap() map[string]any {
	out := map[string]any{}

	if a.Prompt != "" {
		out[aiKeyPrompt] = a.Prompt
	}

	if len(a.Contract) > 0 {
		contract := make([]any, 0, len(a.Contract))
		for _, item := range a.Contract {
			contract = append(contract, item)
		}

		out[aiKeyContract] = contract
	}

	if a.Model != "" {
		out[aiKeyModel] = a.Model
	}

	if a.GeneratedAt != nil {
		out[aiKeyGeneratedAt] = a.GeneratedAt.UTC().Format(time.RFC3339)
	}

	if a.Repair != "" {
		out[aiKeyRepair] = string(a.Repair)
	}

	return out
}

// Validate checks the block: a prompt needs a non-empty contract, and repair
// is one of off, propose, auto.
func (a *AIConfig) Validate() error {
	if a.Repair != "" && !slices.Contains(RepairModes(), a.Repair) {
		return aiFieldError(aiKeyRepair, fmt.Sprintf("must be one of off, propose, auto, got %q", a.Repair))
	}

	if len(a.Prompt) > maxPromptLength {
		return aiFieldError(aiKeyPrompt, fmt.Sprintf("must be at most %d characters", maxPromptLength))
	}

	if strings.TrimSpace(a.Prompt) != "" && len(a.Contract) == 0 {
		return aiFieldError(aiKeyContract, "must not be empty when a prompt is set")
	}

	if len(a.Contract) > maxContractEntries {
		return aiFieldError(aiKeyContract, fmt.Sprintf("must have at most %d entries", maxContractEntries))
	}

	for _, item := range a.Contract {
		if strings.TrimSpace(item) == "" {
			return aiFieldError(aiKeyContract, "entries must not be empty")
		}

		if len(item) > maxContractLength {
			return aiFieldError(aiKeyContract, fmt.Sprintf("entries must be at most %d characters", maxContractLength))
		}
	}

	return nil
}
