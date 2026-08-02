package msteams

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStripMention covers the Teams-specific half of the parser: everything
// Teams wraps around a command line before we ever get to tokenizing.
func TestStripMention(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "plain mention",
			in:   "<at>SolidPing</at> checks list",
			want: "checks list",
		},
		{
			name: "mention with attributes",
			in:   `<at id="0">SolidPing</at> checks list`,
			want: "checks list",
		},
		{
			// A self-hosted operator may rename the app; stripping the tag
			// rather than the literal name keeps commands working.
			name: "renamed app",
			in:   "<at>Ops Monitor (dev)</at> incidents list",
			want: "incidents list",
		},
		{
			name: "non-breaking space after mention",
			in:   "<at>SolidPing</at>&nbsp;checks list",
			want: "checks list",
		},
		{
			name: "html wrapped rich text",
			in:   "<p><at>SolidPing</at> checks list</p>",
			want: "checks list",
		},
		{
			name: "anchor becomes bare url",
			in:   `<at>SolidPing</at> checks add <a href="https://example.com">example.com</a>`,
			want: "checks add https://example.com",
		},
		{
			name: "mention only",
			in:   "<at>SolidPing</at>",
			want: "",
		},
		{
			name: "no mention at all",
			in:   "checks list",
			want: "checks list",
		},
		{
			// Escaped markup in a user's own text must stay literal text and
			// never be re-interpreted as a mention tag.
			name: "escaped markup is not re-parsed",
			in:   "<at>SolidPing</at> checks add https://x.test/&lt;at&gt;y&lt;/at&gt;",
			want: "checks add https://x.test/<at>y</at>",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			require.New(t).Equal(tc.want, StripMention(tc.in))
		})
	}
}

func TestParseMentionText_Commands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		in         string
		command    string
		subcommand string
		args       []string
		flags      map[string]string
	}{
		{
			name:    "empty text is help",
			in:      "<at>SolidPing</at>",
			command: cmdHelp,
		},
		{
			name:    "explicit help",
			in:      "<at>SolidPing</at> help",
			command: cmdHelp,
		},
		{
			name:       "checks add with url",
			in:         "<at>SolidPing</at> checks add https://example.com",
			command:    cmdChecks,
			subcommand: subAdd,
			args:       []string{"https://example.com"},
		},
		{
			name:       "checks add with slug flag",
			in:         "<at>SolidPing</at> checks add https://example.com -slug my-check",
			command:    cmdChecks,
			subcommand: subAdd,
			args:       []string{"https://example.com"},
			flags:      map[string]string{"slug": "my-check"},
		},
		{
			name:       "checks list",
			in:         "<at>SolidPing</at> checks list",
			command:    cmdChecks,
			subcommand: subList,
		},
		{
			name:       "checks rm",
			in:         "<at>SolidPing</at> checks rm my-check",
			command:    cmdChecks,
			subcommand: "rm",
			args:       []string{"my-check"},
		},
		{
			name:       "incidents list filtered",
			in:         "<at>SolidPing</at> incidents list -check my-check",
			command:    cmdIncidents,
			subcommand: subList,
			flags:      map[string]string{"check": "my-check"},
		},
		{
			name:    "results with flag",
			in:      "<at>SolidPing</at> results -check my-check",
			command: cmdResults,
			flags:   map[string]string{"check": "my-check"},
		},
		{
			name:       "config default-channel",
			in:         "<at>SolidPing</at> config default-channel",
			command:    cmdConfig,
			subcommand: subDefaultChannel,
		},
		{
			name:    "command case is normalized",
			in:      "<at>SolidPing</at> CHECKS LIST",
			command: cmdChecks,
			// Subcommand is lowercased too.
			subcommand: subList,
		},
		{
			name:       "quoted argument stays one token",
			in:         `<at>SolidPing</at> checks add "https://example.com/a b"`,
			command:    cmdChecks,
			subcommand: subAdd,
			args:       []string{"https://example.com/a b"},
		},
		{
			name:    "boolean flag without value",
			in:      "<at>SolidPing</at> results -check",
			command: cmdResults,
			flags:   map[string]string{"check": "true"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)

			cmd := ParseMentionText(tc.in)
			r.Equal(tc.command, cmd.Command)
			r.Equal(tc.subcommand, cmd.Subcommand)

			if tc.args == nil {
				r.Empty(cmd.Args)
			} else {
				r.Equal(tc.args, cmd.Args)
			}

			for key, want := range tc.flags {
				r.Equal(want, cmd.Flags[key], "flag %q", key)
			}

			if tc.flags == nil {
				r.Empty(cmd.Flags)
			}
		})
	}
}

// botMention builds the entity list Teams sends alongside a mention of the
// bot itself.
func botMention(text string) []Mention {
	return []Mention{{
		Type:      "mention",
		Text:      text,
		Mentioned: &ChannelAccount{ID: "28:" + testAppID, Name: "SolidPing"},
	}}
}

// TestStripRecipientMention_KeepsOtherPeoplesMentions is the correctness
// control for entity-scoped stripping.
//
// The un-anchored strip deletes EVERY `<at>…</at>` run, so a command that
// legitimately quotes another person's mention would lose that token and the
// remaining arguments would shift — the bot would then act on the wrong
// value. Only the bot's own mention may be removed.
func TestStripRecipientMention_KeepsOtherPeoplesMentions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	text := "<at>SolidPing</at> checks rm <at>Bob</at>"
	entities := botMention("<at>SolidPing</at>")

	r.Equal("checks rm Bob", StripRecipientMention(text, entities, "28:"+testAppID))

	// The un-anchored fallback is what this fixes — pinned so a regression is
	// visible rather than silent.
	r.Equal("checks rm", StripMention(text))
}

// TestParseActivityCommand_ArgsSurviveForeignMentions proves the fix at the
// level that matters: the parsed argument list.
func TestParseActivityCommand_ArgsSurviveForeignMentions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	activity := &Activity{
		Type:      ActivityTypeMessage,
		Text:      "<at>SolidPing</at> checks rm <at>Bob</at>",
		Recipient: &ChannelAccount{ID: "28:" + testAppID},
		Entities:  botMention("<at>SolidPing</at>"),
	}

	cmd := ParseActivityCommand(activity)
	r.Equal(cmdChecks, cmd.Command)
	r.Equal("rm", cmd.Subcommand)
	r.Equal([]string{"Bob"}, cmd.Args)
}

// TestParseActivityCommand_FallsBackWithoutEntities keeps activities that
// carry no entity list working (older channels, emulator traffic).
func TestParseActivityCommand_FallsBackWithoutEntities(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	activity := &Activity{
		Type:      ActivityTypeMessage,
		Text:      "<at>SolidPing</at> checks list",
		Recipient: &ChannelAccount{ID: "28:" + testAppID},
	}

	cmd := ParseActivityCommand(activity)
	r.Equal(cmdChecks, cmd.Command)
	r.Equal(subList, cmd.Subcommand)
}

// TestStripRecipientMention_IgnoresOtherBotsMentions covers the case where the
// entity list names a mention that is NOT us: nothing of ours to strip, and
// the other mention must survive as text.
func TestStripRecipientMention_IgnoresOtherBotsMentions(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	entities := []Mention{{
		Type:      "mention",
		Text:      "<at>SomeoneElse</at>",
		Mentioned: &ChannelAccount{ID: "28:another-app"},
	}}

	got := StripRecipientMention("<at>SomeoneElse</at> checks list", entities, "28:"+testAppID)
	r.Equal("checks list", got)
}
