package checkjs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// runBounded runs a script with a 30 s check timeout but waits at most `wait`
// for it, so a regexp the limit misses fails the test instead of hanging it.
func runBounded(t *testing.T, script string, wait time.Duration) (*checkerdef.Result, time.Duration) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), wait)
	defer cancel()

	type outcome struct {
		result *checkerdef.Result
		err    error
	}

	done := make(chan outcome, 1)
	start := time.Now()

	go func() {
		result, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
			Script:  script,
			Timeout: 30 * time.Second,
		})
		done <- outcome{result, err}
	}()

	select {
	case out := <-done:
		require.NoError(t, out.err)

		return out.result, time.Since(start)
	case <-ctx.Done():
		t.Fatalf("the run did not return within %v: the regexp match is not bounded", wait)

		return nil, 0
	}
}

// TestRegexpCatastrophicBacktrackingIsBounded: a lookahead forces goja onto
// regexp2, and a nested quantifier on a non-matching input backtracks
// exponentially inside one native call that vm.Interrupt cannot reach
// (spec 2026-11-01-02). The per-match limit turns it into an ERROR.
func TestRegexpCatastrophicBacktrackingIsBounded(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, elapsed := runBounded(t, `
var input = "a".repeat(30) + "!";
var hit = /^(?=a)(a+)+$/.test(input);
return { status: "up", output: { hit: hit } };
`, 15*time.Second)

	r.Equal(checkerdef.StatusError, result.Status, "output: %v", result.Output)
	r.Contains(result.Output["error"], "regular expression took too long")
	r.NotContains(result.Output["error"], "aaaaaaaaaa", "the input must not be echoed")
	r.Less(elapsed, 10*time.Second)
}

// TestRegexpBacktrackingInMethods: every String/RegExp method that runs a
// regexp2 pattern surfaces the limit as a thrown error, not a silent miss.
func TestRegexpBacktrackingInMethods(t *testing.T) {
	t.Parallel()

	calls := map[string]string{
		"exec":     `re.exec(input);`,
		"match":    `input.match(re);`,
		"matchAll": `Array.from(input.matchAll(new RegExp(re.source, "g")));`,
		"replace":  `input.replace(re, "x");`,
		"split":    `input.split(re);`,
		"search":   `input.search(re);`,
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			result, _ := runBounded(t, `
var input = "a".repeat(30) + "!";
var re = /^(?=a)(a+)+$/;
`+call+`
return { status: "up" };
`, 15*time.Second)

			r.Equal(checkerdef.StatusError, result.Status, "output: %v", result.Output)
			r.Contains(result.Output["error"], "regular expression took too long")
		})
	}
}

// TestRegexpLookaroundAndBackreferenceStillWork: the limit does not get in the
// way of a legitimate regexp2 pattern.
func TestRegexpLookaroundAndBackreferenceStillWork(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, _ := runBounded(t, `
var header = 'server: "nginx/1.25.3"';
var m = /(["'])(?=nginx)nginx\/(\d+\.\d+\.\d+)\1/.exec(header);
return { status: "up", output: { version: m ? m[2] : null } };
`, 10*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal("1.25.3", result.Output["version"])
}

// TestRegexpRE2PatternUnchanged: an RE2-compatible pattern runs on Go's
// regexp, as before.
func TestRegexpRE2PatternUnchanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, _ := runBounded(t, `
var m = "build 2026-11-01 ok".match(/(\d{4})-(\d{2})-(\d{2})/);
var parts = "a,b;c".split(/[,;]/);
return { status: "up", output: { year: m[1], parts: parts.length } };
`, 10*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal("2026", result.Output["year"])
	r.EqualValues(3, result.Output["parts"])
}
