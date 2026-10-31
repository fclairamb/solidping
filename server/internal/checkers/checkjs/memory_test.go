package checkjs

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

// TestMemoryBudgetRefusesNativeAmplifiers: a single native call asked for a
// result far over the budget is refused before it allocates (spec
// 2026-10-31-01). goja's interrupt does not reach inside such a call, so
// without the cap these ran to completion whatever the timeout. 512 MiB stays
// harmless even when the cap is missing: the test then fails on its status
// and its duration instead of taking the process down.
func TestMemoryBudgetRefusesNativeAmplifiers(t *testing.T) {
	t.Parallel()

	const (
		huge  = "512 * 1024 * 1024"
		slots = "8 * 1024 * 1024"
	)

	tests := []struct {
		name   string
		script string
	}{
		{"repeat", `var s = "x".repeat(` + huge + `);`},
		{"padStart", `var s = "".padStart(` + huge + `, "ab");`},
		{"padEnd", `var s = "a".padEnd(` + huge + `);`},
		{"Array(n).join", `var s = Array(` + huge + `).join("x");`},
		{"sparse join", `var a = []; a.length = ` + huge + `; var s = a.join("x");`},
		{"sparse join empty separator", `var a = []; a.length = 4e9; var s = a.join("");`},
		{"ArrayBuffer then fill", `var s = new Uint8Array(new ArrayBuffer(` + huge + `)).fill(1);`},
		{"typed array then fill", `var s = new Uint8Array(` + huge + `).fill(1);`},
		{"Float64Array", `var s = new Float64Array(` + huge + ` / 8);`},
		{"constructor bypass", `var s = new (new ArrayBuffer(0).constructor)(` + huge + `);`},
		{"typed array constructor bypass", `var s = new (new Uint8Array(0).constructor)(` + huge + `);`},
		{"typed array from", `var s = Uint8Array.from({ length: ` + huge + ` });`},
		{"typed array subclass", `class Big extends Uint8Array {} var s = new Big(` + huge + `);`},
		{"Array.from array-like", `var s = Array.from({ length: ` + huge + ` });`},
		{"sparse fill", `var a = []; a.length = ` + huge + `; var s = a.fill(0);`},
		{"typed array join", `var s = new Uint8Array(1024 * 1024).join("x".repeat(64));`},
		// Length writes cost nothing, but these builtins preallocate from
		// length: 8 Mi slots are 128 MiB, past the budget yet harmless.
		{"holey sort", `var a = [2, 1]; a.length = ` + slots + `; a.sort();`},
		{"holey toSorted", `var a = [2, 1]; a.length = ` + slots + `; var s = a.toSorted();`},
		{"array-like sort", `Array.prototype.sort.call({ length: ` + slots + ` });`},
		{"array-like toSorted", `var s = Array.prototype.toSorted.call({ length: ` + slots + ` });`},
		{"holey map", `var a = [1]; a.length = ` + slots + `; var s = a.map(function (x) { return x; });`},
		{"holey with", `var a = [1]; a.length = ` + slots + `; var s = a.with(0, 2);`},
		{"holey toReversed", `var a = [1]; a.length = ` + slots + `; var s = a.toReversed();`},
		{"holey toSpliced", `var a = [1]; a.length = ` + slots + `; var s = a.toSpliced(0, 0, 1);`},
		{"huge length sort", `var a = []; a.length = 4e9; a.sort();`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			start := time.Now()
			result, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
				Script:  tt.script + "\nreturn { status: \"up\" };",
				Timeout: 2 * time.Second,
			})
			elapsed := time.Since(start)

			r.NoError(err)
			r.Equal(checkerdef.StatusError, result.Status, "output: %v", result.Output)
			r.Contains(result.Output["error"], "RangeError: memory budget exceeded")
			r.Less(elapsed, time.Second, "the refusal must come before the allocation")
		})
	}
}

// TestMemoryBudgetReadsLengthOnce: an array-like whose length getter answers
// 0 to the charge and 4e9 afterwards is read once, so goja works on the length
// that was charged instead of preallocating from the second answer.
func TestMemoryBudgetReadsLengthOnce(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := time.Now()
	result := runScript(t, `
function like() { var n = 0; return { get length() { return n++ ? 4e9 : 0; } }; }
var o = like();
Array.prototype.sort.call(o);
return {
  status: "up",
  output: {
    toSorted: Array.prototype.toSorted.call(like()).length,
    toReversed: Array.prototype.toReversed.call(like()).length,
    from: Array.from(like()).length,
    sortedReads: String(o.length),
  },
};
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.EqualValues(0, result.Output["toSorted"])
	r.EqualValues(0, result.Output["toReversed"])
	r.EqualValues(0, result.Output["from"])
	r.Equal("4000000000", result.Output["sortedReads"], "the getter ran once inside sort")
	r.Less(time.Since(start), time.Second)
}

// TestMemoryBudgetIsCumulative: allocations each under the budget add up, so
// a loop of medium ones is refused once their sum passes it.
func TestMemoryBudgetIsCumulative(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
var kept = [];
var made = 0;
var refusal = "";
for (var i = 0; i < 40; i++) {
  try {
    kept.push("x".repeat(4 * 1024 * 1024));
    made++;
  } catch (e) {
    refusal = e.name + ": " + e.message;
    break;
  }
}
return { status: "up", output: { made: made, refusal: refusal } };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.EqualValues(16, result.Output["made"], "64 MiB / 4 MiB")
	r.Contains(result.Output["refusal"], "RangeError: memory budget exceeded")
}

// TestMemoryBudgetRefusalIgnoresReassignedRangeError: the refusal uses the
// RangeError captured before the script ran.
func TestMemoryBudgetRefusalIgnoresReassignedRangeError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
RangeError = function () { return {}; };
try {
  "x".repeat(1e12);
} catch (e) {
  return { status: "up", output: { isError: e instanceof Error, message: String(e.message) } };
}
return { status: "down" };
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal(true, result.Output["isError"])
	r.Contains(result.Output["message"], "memory budget exceeded")
}

// TestMemoryBudgetPositiveControl: ordinary use of the capped builtins keeps
// working, with the same results and the same object model.
func TestMemoryBudgetPositiveControl(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
var buf = new ArrayBuffer(8);
var view = new Uint8Array(buf);
view.set([1, 2, 3]);
var nested = [1, [2, 3]];
var cyclic = [1];
cyclic.push(cyclic);
class Bytes extends Uint8Array {}
var b = new Bytes(4);
var unicode = ["é", "€"].join("·");
function sortSame() { var a = [2, 1]; return a.sort() === a; }
function likeSort() {
  var o = { length: 3, 0: "c", 1: "a", 2: "b" };
  return (Array.prototype.sort.call(o) === o) + ":" + o[0] + o[1] + o[2] + ":" + o.length;
}
return {
  status: "up",
  output: {
    repeat: "ab".repeat(3),
    padStart: "5".padStart(3, "0"),
    padEnd: "ab".padEnd(4),
    join: ["a", 1, null, undefined, true].join("-"),
    defaultJoin: [1, 2, 3].join(),
    nested: String(nested),
    cyclic: String(cyclic),
    unicode: unicode,
    typed: Array.from(view).join(","),
    typedJoin: view.join("|"),
    fill: new Array(3).fill(7).join(","),
    arrayOf: Array(2).length + "/" + new Array(1, 2, 3).length,
    from: Array.from("abc").join(""),
    viewShared: view.buffer === buf,
    sliceLen: view.slice(1).length,
    subarray: view.subarray(0, 2).byteLength,
    floats: new Float64Array([1.5, 2.5]).join(","),
    instanceofArray: [] instanceof Array,
    isArray: Array.isArray([1]),
    instanceofBuffer: buf instanceof ArrayBuffer,
    instanceofTyped: view instanceof Uint8Array,
    subclass: b instanceof Uint8Array && b instanceof Bytes && b.length === 4,
    bpe: Uint8Array.BYTES_PER_ELEMENT + Float64Array.BYTES_PER_ELEMENT,
    ctorName: ArrayBuffer.name + "/" + Uint8Array.name,
    repeatLength: "".repeat.length,
    sorted: [3, 1, 2].sort().join(","),
    sortSame: sortSame(),
    likeSort: likeSort(),
    toSorted: [3, 1, 2].toSorted(function (a, b) { return b - a; }).join(","),
    mapped: [1, 2, 3].map(function (x) { return x * 2; }).join(","),
    withed: [1, 2, 3].with(-1, 9).join(","),
    reversed: [1, 2, 3].toReversed().join(","),
    spliced: [1, 2, 3, 4].toSpliced(1, 2, "x").join(","),
    argsSorted: (function () { return Array.prototype.toSorted.call(arguments).join(","); })(3, 1, 2),
    fromLike: Array.from({ length: 2, 0: "a", 1: "b" }).join(""),
    fromSet: Array.from(new Set([1, 2])).join(","),
  },
};
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)

	out := result.Output
	r.Equal("ababab", out["repeat"])
	r.Equal("005", out["padStart"])
	r.Equal("ab  ", out["padEnd"])
	r.Equal("a-1---true", out["join"])
	r.Equal("1,2,3", out["defaultJoin"])
	r.Equal("1,2,3", out["nested"])
	r.Equal("1,", out["cyclic"])
	r.Equal("é·€", out["unicode"])
	r.Equal("1,2,3,0,0,0,0,0", out["typed"])
	r.Equal("1|2|3|0|0|0|0|0", out["typedJoin"])
	r.Equal("7,7,7", out["fill"])
	r.Equal("2/3", out["arrayOf"])
	r.Equal("abc", out["from"])
	r.Equal(true, out["viewShared"])
	r.EqualValues(7, out["sliceLen"])
	r.EqualValues(2, out["subarray"])
	r.Equal("1.5,2.5", out["floats"])
	r.Equal(true, out["instanceofArray"])
	r.Equal(true, out["isArray"])
	r.Equal(true, out["instanceofBuffer"])
	r.Equal(true, out["instanceofTyped"])
	r.Equal(true, out["subclass"])
	r.EqualValues(9, out["bpe"])
	r.Equal("ArrayBuffer/Uint8Array", out["ctorName"])
	r.EqualValues(1, out["repeatLength"])
	r.Equal("1,2,3", out["sorted"])
	r.Equal(true, out["sortSame"])
	r.Equal("true:abc:3", out["likeSort"])
	r.Equal("3,2,1", out["toSorted"])
	r.Equal("2,4,6", out["mapped"])
	r.Equal("1,2,9", out["withed"])
	r.Equal("3,2,1", out["reversed"])
	r.Equal("1,x,4", out["spliced"])
	r.Equal("1,2,3", out["argsSorted"])
	r.Equal("ab", out["fromLike"])
	r.Equal("1,2", out["fromSet"])
}

// TestMemoryBudgetKeepsOriginalErrors: arguments the builtins reject still
// throw their own error, not a budget refusal.
func TestMemoryBudgetKeepsOriginalErrors(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
function msg(f) { try { f(); return "none"; } catch (e) { return e.name; } }
return {
  status: "up",
  output: {
    negRepeat: msg(function () { "x".repeat(-1); }),
    infRepeat: msg(function () { "x".repeat(Infinity); }),
    badLength: msg(function () { new Array(-1); }),
    noNew: msg(function () { ArrayBuffer(4); }),
    nullThis: msg(function () { String.prototype.repeat.call(null, 2); }),
    badIndex: msg(function () { [1].with(5, 0); }),
    badCompare: msg(function () { [1].toSorted(1); }),
    sortNullThis: msg(function () { Array.prototype.sort.call(null); }),
  },
};
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal("RangeError", result.Output["negRepeat"])
	r.Equal("RangeError", result.Output["infRepeat"])
	r.Equal("RangeError", result.Output["badLength"])
	r.Equal("TypeError", result.Output["noNew"])
	r.Equal("TypeError", result.Output["nullThis"])
	r.Equal("RangeError", result.Output["badIndex"])
	r.Equal("TypeError", result.Output["badCompare"])
	r.Equal("TypeError", result.Output["sortNullThis"])
}
