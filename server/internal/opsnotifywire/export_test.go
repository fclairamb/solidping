package opsnotifywire

import "github.com/fclairamb/solidping/server/internal/integrations/discord"

// SetDiscordBotClientFactory points the Discord bot client at a stand-in for the
// duration of a test, and restores it afterwards.
func SetDiscordBotClientFactory(factory func(token string) *discord.BotClient) func() {
	previous := newDiscordBotClient
	newDiscordBotClient = factory

	return func() { newDiscordBotClient = previous }
}
