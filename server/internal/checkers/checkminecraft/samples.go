package checkminecraft

import (
	"time"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

const sampleHost = "play.example.com"

// GetSampleConfigs returns sample Minecraft check configurations.
func (c *MinecraftChecker) GetSampleConfigs(_ *checkerdef.ListSampleOptions) []checkerdef.CheckSpec {
	return []checkerdef.CheckSpec{
		{
			Name:   "Minecraft Java Server",
			Slug:   "minecraft-java",
			Period: 5 * time.Minute,
			Config: (&MinecraftConfig{
				Host:    sampleHost,
				Edition: EditionJava,
			}).GetConfig(),
		},
		{
			Name:   "Minecraft Bedrock Server",
			Slug:   "minecraft-bedrock",
			Period: 5 * time.Minute,
			Config: (&MinecraftConfig{
				Host:    "bedrock.example.com",
				Edition: EditionBedrock,
			}).GetConfig(),
		},
	}
}
