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

	const huge = "512 * 1024 * 1024"

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
  },
};
`)

	r.Equal(checkerdef.StatusUp, result.Status, "output: %v", result.Output)
	r.Equal("RangeError", result.Output["negRepeat"])
	r.Equal("RangeError", result.Output["infRepeat"])
	r.Equal("RangeError", result.Output["badLength"])
	r.Equal("TypeError", result.Output["noNew"])
	r.Equal("TypeError", result.Output["nullThis"])
}
