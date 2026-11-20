package checks

import "testing"

func TestStripCopySuffix(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"acme":           "acme",
		"acme-copy":      "acme",
		"acme-copy-2":    "acme",
		"acme-copy-copy": "acme-copy",
		"acme-copy-x":    "acme-copy-x",
		"-copy":          "-copy",
	}
	for in, want := range cases {
		if got := stripCopySuffix(in); got != want {
			t.Errorf("stripCopySuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestStripCopyNameSuffix(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"Acme":          "Acme",
		"Acme (copy)":   "Acme",
		"Acme (copy) x": "Acme (copy) x",
	}
	for in, want := range cases {
		if got := stripCopyNameSuffix(in); got != want {
			t.Errorf("stripCopyNameSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
