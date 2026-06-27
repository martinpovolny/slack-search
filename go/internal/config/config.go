package config

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	JiraURL      string   `json:"jira_url"`
	JiraProjects []string `json:"jira_projects"`
}

// Load reads config from ~/.slack-search/config.json. Returns zero config if missing.
func Load() Config {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}
	}
	data, err := os.ReadFile(filepath.Join(home, ".slack-search", "config.json"))
	if err != nil {
		return Config{}
	}
	var c Config
	json.Unmarshal(data, &c)
	return c
}
