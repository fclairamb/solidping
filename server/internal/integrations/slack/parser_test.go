package slack

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseMentionText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected *ParsedCommand
	}{
		{
			name:  "empty message returns help",
			input: "<@U123ABC>",
			expected: &ParsedCommand{
				Command: "help",
				Flags:   make(map[string]string),
			},
		},
		{
			name:  "help command",
			input: "<@U123ABC> help",
			expected: &ParsedCommand{
				Command: "help",
				Flags:   make(map[string]string),
			},
		},
		{
			name:  "checks list",
			input: "<@U123ABC> checks list",
			expected: &ParsedCommand{
				Command:    "checks",
				Subcommand: "list",
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "checks add with url",
			input: "<@U123ABC> checks add https://example.com",
			expected: &ParsedCommand{
				Command:    "checks",
				Subcommand: "add",
				Args:       []string{"https://example.com"},
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "checks add with Slack-formatted url",
			input: "<@U123ABC> checks add <https://www.google.fr|www.google.fr>",
			expected: &ParsedCommand{
				Command:    "checks",
				Subcommand: "add",
				Args:       []string{"https://www.google.fr"},
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "checks add with Slack-formatted url no display text",
			input: "<@U123ABC> checks add <https://example.com>",
			expected: &ParsedCommand{
				Command:    "checks",
				Subcommand: "add",
				Args:       []string{"https://example.com"},
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "checks add with flags",
			input: "<@U123ABC> checks add https://example.com -slug mycheck -interval 30s",
			expected: &ParsedCommand{
				Command:    "checks",
				Subcommand: "add",
				Args:       []string{"https://example.com"},
				Flags: map[string]string{
					"slug":     "mycheck",
					"interval": "30s",
				},
			},
		},
		{
			name:  "checks rm",
			input: "<@U123ABC> checks rm my-check",
			expected: &ParsedCommand{
				Command:    "checks",
				Subcommand: "rm",
				Args:       []string{"my-check"},
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "results with check flag",
			input: "<@U123ABC> results -check my-check",
			expected: &ParsedCommand{
				Command: "results",
				Flags: map[string]string{
					"check": "my-check",
				},
			},
		},
		{
			name:  "incidents list",
			input: "<@U123ABC> incidents list",
			expected: &ParsedCommand{
				Command:    "incidents",
				Subcommand: "list",
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "incidents list with check filter",
			input: "<@U123ABC> incidents list -check my-check",
			expected: &ParsedCommand{
				Command:    "incidents",
				Subcommand: "list",
				Flags: map[string]string{
					"check": "my-check",
				},
			},
		},
		{
			name:  "config default-channel",
			input: "<@U123ABC> config default-channel <#C12345|random>",
			expected: &ParsedCommand{
				Command:    "config",
				Subcommand: "default-channel",
				Args:       []string{"<#C12345|random>"},
				Flags:      make(map[string]string),
			},
		},
		{
			name:  "unknown command",
			input: "<@U123ABC> foobar",
			expected: &ParsedCommand{
				Command: "foobar",
				Flags:   make(map[string]string),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result := ParseMentionText(tt.input)

			r.Equal(tt.expected.Command, result.Command, "command mismatch")
			r.Equal(tt.expected.Subcommand, result.Subcommand, "subcommand mismatch")
			r.Equal(tt.expected.Args, result.Args, "args mismatch")
			r.Equal(tt.expected.Flags, result.Flags, "flags mismatch")
		})
	}
}

func TestExtractSlackLinks(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "link with display text",
			input:    "<https://www.google.fr|www.google.fr>",
			expected: "https://www.google.fr",
		},
		{
			name:     "link without display text",
			input:    "<https://example.com>",
			expected: "https://example.com",
		},
		{
			name:     "http link",
			input:    "<http://example.com|example>",
			expected: "http://example.com",
		},
		{
			name:     "plain url unchanged",
			input:    "https://example.com",
			expected: "https://example.com",
		},
		{
			name:     "multiple links",
			input:    "check <https://a.com|a> and <https://b.com>",
			expected: "check https://a.com and https://b.com",
		},
		{
			name:     "link in command context",
			input:    "checks add <https://www.google.fr|www.google.fr> -slug test",
			expected: "checks add https://www.google.fr -slug test",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result := extractSlackLinks(tt.input)
			r.Equal(tt.expected, result)
		})
	}
}

func TestTokenize(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "simple tokens",
			input:    "hello world",
			expected: []string{"hello", "world"},
		},
		{
			name:     "quoted string",
			input:    `hello "world with spaces" foo`,
			expected: []string{"hello", "world with spaces", "foo"},
		},
		{
			name:     "single quoted string",
			input:    `hello 'world with spaces' foo`,
			expected: []string{"hello", "world with spaces", "foo"},
		},
		{
			name:     "multiple spaces",
			input:    "hello    world",
			expected: []string{"hello", "world"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			result := tokenize(tt.input)
			r.Equal(tt.expected, result)
		})
	}
}
