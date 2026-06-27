package nlq

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RHTConfig holds the RHT models.corp configuration.
type RHTConfig struct {
	URLTemplate string
	Models      map[string]RHTModel
}

// RHTModel holds a single model's config.
type RHTModel struct {
	Key        string `json:"key"`
	APIModelID string `json:"api_model_id"`
}

// LoadRHTConfig reads .rht_models.json from the project root or ~/.slack-search/.
func LoadRHTConfig() (*RHTConfig, error) {
	paths := []string{
		".rht_models.json",
	}

	home, err := os.UserHomeDir()
	if err == nil {
		paths = append(paths, filepath.Join(home, ".slack-search", ".rht_models.json"))
	}

	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var raw struct {
			URLTemplate string                `json:"url_template"`
			Models      map[string]RHTModel   `json:"models"`
		}
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse %s: %w", p, err)
		}
		return &RHTConfig{
			URLTemplate: raw.URLTemplate,
			Models:      raw.Models,
		}, nil
	}

	return nil, fmt.Errorf(".rht_models.json not found")
}

// Endpoint returns the base URL and API key for a given model name.
func (c *RHTConfig) Endpoint(modelName string) (baseURL, apiKey, apiModelID string, err error) {
	model, ok := c.Models[modelName]
	if !ok {
		available := make([]string, 0, len(c.Models))
		for k := range c.Models {
			available = append(available, k)
		}
		return "", "", "", fmt.Errorf("model %q not found in .rht_models.json (available: %s)", modelName, strings.Join(available, ", "))
	}

	baseURL = strings.ReplaceAll(c.URLTemplate, "{model}", modelName)
	apiKey = model.Key
	apiModelID = model.APIModelID
	if apiModelID == "" {
		apiModelID = modelName
	}
	return baseURL, apiKey, apiModelID, nil
}
