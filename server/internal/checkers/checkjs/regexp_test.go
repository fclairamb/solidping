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

	return runBoundedTimeout(t, script, 30*time.Second, wait)
}

// runBoundedTimeout is runBounded with the check timeout given.
func runBoundedTimeout(
	t *testing.T, script string, timeout, wait time.Duration,
) (*checkerdef.Result, time.Duration) {
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
			Timeout: timeout,
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

// slowSegments builds a script whose regexp takes about 0.1 s per match: at
// the start of every "aaa…a;" segment, (a+)+! backtracks over 20 a's before
// a+; matches the segment. 50 segments are about 5 s of matching.
func slowSegments(call string) string {
	return `
var input = ("a".repeat(20) + ";").repeat(50);
var re = /(?:(a+)+!|a+;)/g;
` + call + `
return { status: "up" };
`
}

// TestRegexpGlobalLoopHonoursTimeout: a global replace, match, matchAll or
// a split runs its match loop inside one native call, and the per-match
// limit gives each match a fresh 2 s: 50 slow matches ran for the sum of
// them, past the check's timeout (spec 2026-11-02-02). Each match now checks
// the run's deadline first.
func TestRegexpGlobalLoopHonoursTimeout(t *testing.T) {
	t.Parallel()

	calls := map[string]string{
		"replace":          `input.replace(re, "x");`,
		"replace function": `input.replace(re, function (m) { return m; });`,
		"replaceAll":       `input.replaceAll(re, "x");`,
		"match":            `input.match(re);`,
		"matchAll":         `Array.from(input.matchAll(re));`,
		"split":            `input.split(new RegExp(re.source));`,
		"symbol split":     `re[Symbol.split](input);`,
		"split species": `
class R extends RegExp { static get [Symbol.species]() { return RegExp; } }
input.split(new R(re.source));`,
		"split subclass": `
class R extends RegExp {}
input.split(new R(re.source));`,
	}

	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			result, elapsed := runBoundedTimeout(t, slowSegments(call), 500*time.Millisecond, 20*time.Second)

			r.Contains([]checkerdef.Status{checkerdef.StatusTimeout, checkerdef.StatusError}, result.Status,
				"output: %v", result.Output)
			r.Less(elapsed, 3*time.Second, "the loop ran past the check's timeout")
		})
	}
}

// TestRegexpGlobalLoopResults: the loops moved onto exec give goja's
// results, lookaround patterns included.
func TestRegexpGlobalLoopResults(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result, _ := runBounded(t, `
var re = /(?=\d)(\d)/g;
re.lastIndex = 3;
var replaced = "a1b2c3".replace(re, "#");
var after = re.lastIndex;
var fn = "a1b2c3".replace(re, function (m, d, pos) { return "<" + d + pos + ">"; });
var all = "a1b2c3".replaceAll(re, "[$1]");
var matched = "a1b2c3".match(re).join("|");
var none = "abc".match(re);
var iter = Array.from("a1b2".matchAll(re), function (m) { return m[0] + "@" + m.index; }).join("|");
var sticky = "x1".replace(/(?=\d)\d/y, "#");
var single = /(?<=a)b/.exec("cab")[0];
var named = "2026-11-02".replace(/(?<y>\d+)-(?<m>\d+)-(?<d>\d+)/g, "$<d>/$<m>/$<y>");
var empty = "abc".replace(/(?:)/g, "-");
var emoji = "a\u{1F600}b".replace(/(?:)/gu, "-");
function j(a) { return JSON.stringify(a); }
return { status: "up", output: {
	replaced: replaced, after: after, fn: fn, all: all, matched: matched, none: none, iter: iter,
	sticky: sticky, single: single, named: named, empty: empty, emoji: emoji,
	split1: j("a1b22c".split(/(?=\d)\d+/)),
	split2: j("a,b;c".split(/([,;])/, 3)),
	split3: j("abc".split(/(?:)/)),
	split4: j("".split(/x/)),
	split5: j("".split(/(?:)/)),
	split6: j("a1b".split(/(\d)|(x)/)),
	split7: j("a1b2".split(/\d/, 0)),
	split8: j("ab".split(/$/)),
	split9: j("a\u{1F600}b".split(/(?:)/u)),
	split10: j((function () { class R extends RegExp {} return "a-b-c".split(new R("-")); })()),
	split11: j((function () { class R extends RegExp {} return "a1b2".split(new R("(\\d)"), 2); })()),
	split12: j("a-b".split(/-/y)),
	split13: j("A-b-C".split(/[a-c]/i)),
}};
`, 10*time.Second)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal(map[string]any{
		"replaced": "a#b#c#",
		"after":    int64(0),
		"fn":       "a<11>b<23>c<35>",
		"all":      "a[1]b[2]c[3]",
		"matched":  "1|2|3",
		"none":     nil,
		"iter":     "1@1|2@3",
		"sticky":   "x1",
		"single":   "b",
		"named":    "02/11/2026",
		"empty":    "-a-b-c-",
		"emoji":    "-a-\U0001F600-b-",
		"split1":   `["a","b","c"]`,
		"split2":   `["a",",","b"]`,
		"split3":   `["a","b","c"]`,
		"split4":   `[""]`,
		"split5":   `[]`,
		"split6":   `["a","1",null,"b"]`,
		"split7":   `[]`,
		"split8":   `["ab"]`,
		"split9":   "[\"a\",\"\U0001F600\",\"b\"]",
		"split10":  `["a","b","c"]`,
		"split11":  `["a","1"]`,
		"split12":  `["a","b"]`,
		"split13":  `["","-","-",""]`,
	}, result.Output)
}
