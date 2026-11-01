package checkjs

import (
	"strings"
	"time"

	"github.com/dlclark/regexp2/v2"
)

// maxRegexpMatchTime bounds a single regular expression match that goja runs
// on regexp2, its backtracking engine (patterns with lookarounds or
// backreferences). goja checks vm.Interrupt only between instructions, so the
// check's timeout never reaches a running match: without this limit a nested
// quantifier on a non-matching input backtracks for hours and leaks a goroutine
// pinned at 100 % of a core (spec 2026-11-01-02). A monitoring regexp finishes
// in microseconds; 2 s stays well under the 30 s check budget.
const maxRegexpMatchTime = 2 * time.Second

// regexpTimeoutMarker starts the error regexp2 returns when a match runs past
// its MatchTimeout; goja rethrows it as a RangeError carrying that text.
const regexpTimeoutMarker = "match timeout after"

// regexpTooLongMessage replaces regexp2's message, which quotes the whole input.
func regexpTooLongMessage() string {
	return "regular expression took too long (over " + maxRegexpMatchTime.String() + " for one match)"
}

// regexp2.Compile copies DefaultMatchTimeout into every Regexp, and goja
// compiles script patterns at run time, so setting it once at process start
// covers every VM: the JS checker, the AI check runner and any other user.
//
//nolint:gochecknoinits // must run before any VM compiles a pattern
func init() {
	regexp2.DefaultMatchTimeout = maxRegexpMatchTime
}

// isRegexpTimeout reports whether a script error is a regexp2 match timeout.
func isRegexpTimeout(err error) bool {
	return err != nil && strings.Contains(err.Error(), regexpTimeoutMarker)
}
