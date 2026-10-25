package checkbrowser

import (
	"testing"

	"github.com/chromedp/chromedp/kb"
	"github.com/stretchr/testify/require"
)

func TestKeySequence(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"Enter": kb.Enter, "return": kb.Enter, "TAB": kb.Tab, "esc": kb.Escape, "Escape": kb.Escape,
		"backspace": kb.Backspace, "delete": kb.Delete, "down": kb.ArrowDown, "ArrowDown": kb.ArrowDown,
		"up": kb.ArrowUp, "arrowup": kb.ArrowUp, "left": kb.ArrowLeft, "ArrowLeft": kb.ArrowLeft,
		"right": kb.ArrowRight, "arrowright": kb.ArrowRight, "a": "a", "F13": "F13",
	}
	for in, want := range tests {
		require.Equal(t, want, keySequence(in), in)
	}
}
