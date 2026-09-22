package config

import "strings"

// The name and slug a docker check gets when the spec leaves them blank. They
// derive purely from the config, so they live here with it.
func resolveSpecName(cfg *DockerConfig) string {
	if cfg.ContainerName != "" {
		return cfg.ContainerName
	}

	return cfg.ContainerID
}

func resolveSpecSlug(cfg *DockerConfig) string {
	if cfg.ContainerName != "" {
		return "docker-" + strings.ReplaceAll(cfg.ContainerName, ".", "-")
	}

	short := cfg.ContainerID
	if len(short) > 12 {
		short = short[:12]
	}

	return "docker-" + short
}
