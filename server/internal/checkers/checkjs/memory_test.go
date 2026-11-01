package checkjs

import (
	"context"
	"testing"
	"time"

	"github.com/dop251/goja"
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
		// rope doubles a 64-character string 22 times: 256 Mi characters
		// that cost nothing until something reads them.
		rope = `var s = "x".repeat(64); for (var i = 0; i < 22; i++) s = s + s; `
	)

	tests := []refusalCase{
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
		// Spec 2026-11-01-01: dense copies, string splitting and replacing,
		// argument lists and ropes built with + are charged too.
		{"split empty", `var s = "x".repeat(` + slots + `).split("");`},
		{"split regexp", `var s = "x".repeat(` + slots + `).split(/(?:)/);`},
		{"replaceAll expanding", `var s = "a".repeat(1024 * 1024).replaceAll("a", "$&".repeat(128));`},
		{"replace suffix per match", `var s = "a".repeat(16 * 1024).replace(/a/g, "$'");`},
		{"replace function returning a big string", `var big = "x".repeat(1024 * 1024);
var s = "a".repeat(100).replace(/a/g, function () { return big; });`},
		{"apply array-like", `Math.max.apply(null, { length: ` + slots + ` });`},
		{"Reflect.apply array-like", `Reflect.apply(Math.max, null, { length: ` + slots + ` });`},
		{"typed array array-like", `var s = new Uint8Array({ length: ` + huge + ` });`},
		{"rope read", rope + `s.charAt(0);`},
		{"rope to a binding", rope + `console.log(s);`},
		{"rope inside a binding argument", rope + `base64.encode({ nested: [s] });`},
		{"rope in the result", rope + `return { status: "up", output: { s: s } };`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			requireRefusal(t, tt.script)
		})
	}
}

// TestMemoryBudgetRefusesHeavyCopies: the amplifiers whose refusal only comes
// after goja has really copied what the budget still allowed (millions of
// slots, 0.3 to 0.5s of CPU each on a quiet machine, several times that on a
// loaded runner with coverage on). That copying is permitted work, so a wall
// clock bound cannot tell it from a refusal that came too late. Each case
// carries a baseline: the same script stopped one step short of the refused
// allocation, which succeeds. The refusal must cost about what the permitted
// work costs on this machine right now, not the refused allocation, which is
// several times bigger (concat doubling would copy 16M slots, not 2M).
//
//nolint:paralleltest // sequential on purpose, the timings must not compete
func TestMemoryBudgetRefusesHeavyCopies(t *testing.T) {
	tests := []heavyCase{
		{
			"concat doubling", `var a = [1]; for (var i = 0; i < 24; i++) a = a.concat(a);`,
			`var a = [1]; for (var i = 0; i < 20; i++) a = a.concat(a);`,
		},
		{"dense slice", `var a = Array.from({ length: 3 * 1024 * 1024 }); var s = a.slice();`, ""},
		{"dense splice", `var a = Array.from({ length: 3 * 1024 * 1024 }); var s = a.splice(0);`, ""},
		{"dense filter", `var a = Array.from({ length: 3 * 1024 * 1024 });
var s = a.filter(function () { return true; });`, ""},
		{
			"flat of a repeated array", `var b = Array.from({ length: 1024 * 1024 }); var s = Array(16).fill(b).flat();`,
			`var b = Array.from({ length: 1024 * 1024 }); var s = Array(1).fill(b).flat();`,
		},
		{
			"flatMap to a repeated array", `var b = Array.from({ length: 1024 * 1024 });
var s = Array(16).fill(0).flatMap(function () { return b; });`,
			`var b = Array.from({ length: 1024 * 1024 });
var s = Array(1).fill(0).flatMap(function () { return b; });`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bound := time.Second
			if tt.baseline != "" {
				start := time.Now()
				result := runScript(t, tt.baseline+"\nreturn { status: \"up\" };")
				require.Equal(t, checkerdef.StatusUp, result.Status, "baseline must fit the budget: %v", result.Output)
				// 3x the permitted work, plus a margin for scheduler noise.
				bound = 3*time.Since(start) + 250*time.Millisecond
			}

			requireRefusalWithin(t, tt.script, bound)
		})
	}
}

// requireRefusal runs the script and requires the memory budget refusal, well
// before the allocation it asks for.
func requireRefusal(t *testing.T, script string) {
	t.Helper()
	requireRefusalWithin(t, script, time.Second)
}

func requireRefusalWithin(t *testing.T, script string, bound time.Duration) {
	t.Helper()
	r := require.New(t)

	start := time.Now()
	result, err := (&JSChecker{}).Execute(context.Background(), &JSConfig{
		Script:  script + "\nreturn { status: \"up\" };",
		Timeout: 10 * time.Second,
	})
	elapsed := time.Since(start)

	r.NoError(err)
	r.Equal(checkerdef.StatusError, result.Status, "output: %v", result.Output)
	r.Contains(result.Output["error"], "RangeError: memory budget exceeded")
	r.Less(elapsed, bound, "the refusal must come before the allocation")
}

type refusalCase struct {
	name   string
	script string
}

// heavyCase is a refusalCase with the permitted part of its script, timed to
// size the bound; "" means the refusal is instant.
type heavyCase struct {
	name     string
	script   string
	baseline string
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

// TestMemoryBudgetPinsLengthsTheGojaReadsAgain: a length getter, or a
// Symbol.isConcatSpreadable getter, that answers one thing to the charge and
// another to goja gets the answer that was charged (spec 2026-11-01-01).
func TestMemoryBudgetPinsLengthsTheGojaReadsAgain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	start := time.Now()
	result := runScript(t, `
function like(first, then) { var n = 0; return { get length() { return n++ ? then : first; } }; }
var refused = "";
try { new Uint8Array(like(512 * 1024 * 1024, 0)); } catch (e) { refused = e.message; }
var spread = like(0, 3e8);
spread[Symbol.isConcatSpreadable] = true;
var flips = 0;
var flip = { length: 3e8 };
Object.defineProperty(flip, Symbol.isConcatSpreadable, { get: function () { return flips++ > 0; } });
var flipped = [].concat(flip);
return {
  status: "up",
  output: {
    typed: new Uint8Array(like(0, 3e8)).length,
    refused: refused,
    concat: [].concat(spread).length,
    flipped: flipped.length,
    flippedSame: flipped[0] === flip,
    apply: Math.max.apply(null, like(0, 3e8)),
  },
};
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.EqualValues(0, result.Output["typed"])
	r.Contains(result.Output["refused"], "memory budget exceeded")
	r.EqualValues(0, result.Output["concat"])
	r.EqualValues(1, result.Output["flipped"])
	r.Equal(true, result.Output["flippedSame"])
	r.Less(time.Since(start), time.Second)
}

// TestMemoryBudgetPositiveControlCopiesAndStrings: ordinary use of concat,
// slice, splice, filter, flat, split, replace, replaceAll, apply, string +
// and typed arrays from a source keeps its results.
func TestMemoryBudgetPositiveControlCopiesAndStrings(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	result := runScript(t, `
var a = [1, 2, 3];
var like = { length: 2, 0: "x", 1: "y" };
var spreadLike = { length: 2, 0: "p", 1: "q" };
spreadLike[Symbol.isConcatSpreadable] = true;
var b = [1, 2, 3, 4];
var removed = b.splice(1, 2, "x", "y", "z");
var holey = [1, , 3].concat([4]);
var rope = "";
for (var i = 0; i < 10; i++) rope = rope + "abcdefghij";
var big = "";
for (var j = 0; j < 8; j++) big = big + "x".repeat(10000);
console.log(big);
class List extends Array {}
return {
  status: "up",
  output: {
    concat: a.concat([4, 5], 6).join(","),
    concatLike: [0].concat(like).length + ":" + ([0].concat(like)[1] === like),
    concatSpread: [0].concat(spreadLike).join(","),
    concatHoles: holey.length + ":" + (1 in holey),
    concatSpecies: new List(1, 2).concat([3]) instanceof List,
    slice: a.slice(1).join(",") + "/" + a.slice(-2, -1).join(",") + "/" + Array.prototype.slice.call(like).join(""),
    splice: removed.join(",") + "/" + b.join(","),
    filter: a.filter(function (x) { return x > 1; }).join(","),
    flat: [1, [2, [3, [4]]]].flat().length + "/" + [1, [2, [3, [4]]]].flat(Infinity).join(",") +
      "/" + [1, , 3].flat().length,
    flatMap: [1, 2].flatMap(function (x) { return [x, x * 2]; }).join(","),
    split: "a,b,c".split(",").length + "/" + "abc".split("").join("|") + "/" + "a1b22c".split(/\d+/).join(",") +
      "/" + "a1b".split(/(\d)/).join(",") + "/" + "a,b,c".split(",", 2).join(",") + "/" + "abc".split().length,
    replace: [
      "hello world".replace("o", "0"),
      "aaa".replace(/a/g, "$&$&"),
      "John Smith".replace(/(\w+)\s(\w+)/, "$2, $1"),
      "x-y".replace("-", "[$\x60|$']"),
      "2026-11-01".replace(/(?<y>\d+)-(?<m>\d+)-(?<d>\d+)/, "$<d>/$<m>/$<y>"),
      "cost".replace("cost", "$$5"),
      "abc".replace(/b/, function (m, off) { return m.toUpperCase() + off; }),
      "a1".replace(/(\d)/, "$2$0$1"),
    ].join(" "),
    replaceAll: "a.b.c".replaceAll(".", "/") + " " + "aXbX".replaceAll(/x/gi, "$&-"),
    rope: rope.length + ":" + rope.charAt(5) + ":" + ("ab" + "cd"),
    bigRope: big.length,
    typed: new Uint8Array([1, 2, 3]).join(",") + "/" + new Uint8Array({ length: 2, 0: 7, 1: 8 }).join(",") +
      "/" + new Uint8Array(new Set([4, 5])).join(",") + "/" + new Uint16Array(new Uint8Array([9, 10])).join(","),
    apply: [
      Math.max.apply(null, [1, 5, 3]),
      Math.max.apply(null, { length: 2, 0: 4, 1: 9 }),
      (function () { return Math.max.apply(null, arguments); })(2, 8),
      Reflect.apply(Math.min, null, [3, 1]),
    ].join(","),
  },
};
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)

	out := result.Output
	r.Equal("1,2,3,4,5,6", out["concat"])
	r.Equal("2:true", out["concatLike"])
	r.Equal("0,p,q", out["concatSpread"])
	r.Equal("4:false", out["concatHoles"])
	r.Equal(true, out["concatSpecies"])
	r.Equal("2,3/2/xy", out["slice"])
	r.Equal("2,3/1,x,y,z,4", out["splice"])
	r.Equal("2,3", out["filter"])
	r.Equal("3/1,2,3,4/2", out["flat"])
	r.Equal("1,2,2,4", out["flatMap"])
	r.Equal("3/a|b|c/a,b,c/a,1,b/a,b/1", out["split"])
	r.Equal("hell0 world aaaaaa Smith, John x[x|y]y 01/11/2026 $5 aB1c a$2$01", out["replace"])
	r.Equal("a/b/c aX-bX-", out["replaceAll"])
	r.Equal("100:f:abcd", out["rope"])
	r.EqualValues(80000, out["bigRope"])
	r.Equal("1,2,3/7,8/4,5/9,10", out["typed"])
	r.Equal("5,9,8,1", out["apply"])
}

// TestRopeDetection pins the goja internal the flatten guard relies on: a
// long `a + b` is an unflattened rope until something reads its characters.
func TestRopeDetection(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	vm := goja.New()
	val, err := vm.RunString(`"x".repeat(70000) + "y"`)
	r.NoError(err)
	r.InDelta(70001, unflattenedRopeLen(val), 0)

	str, ok := val.(goja.String)
	r.True(ok)
	r.Equal(uint16('x'), str.CharAt(0))
	r.InDelta(0, unflattenedRopeLen(val), 0, "flattened")

	small, err := vm.RunString(`"x".repeat(40) + "y"`)
	r.NoError(err)
	r.InDelta(0, unflattenedRopeLen(small), 0, "under ropeFreeLen")
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
