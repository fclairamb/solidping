package systemconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		value        any
		defaultValue bool
		want         bool
	}{
		{name: "native true", value: true, defaultValue: false, want: true},
		{name: "native false", value: false, defaultValue: true, want: false},
		{name: "string true lowercase", value: "true", defaultValue: false, want: true},
		{name: "string true mixed case", value: "TRUE", defaultValue: false, want: true},
		{name: "string false", value: "false", defaultValue: true, want: false},
		{name: "string 1", value: "1", defaultValue: false, want: true},
		{name: "string 0", value: "0", defaultValue: true, want: false},
		{name: "string yes", value: "yes", defaultValue: false, want: true},
		{name: "string no", value: "no", defaultValue: true, want: false},
		{name: "string padded", value: "  true  ", defaultValue: false, want: true},
		{name: "empty string falls back to default true", value: "", defaultValue: true, want: true},
		{name: "empty string falls back to default false", value: "", defaultValue: false, want: false},
		{name: "garbage string falls back to default", value: "maybe", defaultValue: true, want: true},
		{name: "nil falls back to default", value: nil, defaultValue: true, want: true},
		{name: "int falls back to default", value: 1, defaultValue: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			r.Equal(tt.want, parseBool(tt.value, tt.defaultValue))
		})
	}
}
