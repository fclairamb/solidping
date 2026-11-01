package checkjs

import (
	"fmt"
	"math"
	"reflect"

	"github.com/dop251/goja"
)

const (
	// maxScriptAllocBytes is the per-run allocation budget of a script (spec
	// 2026-10-31-01): every native builtin that can build a large value in ONE
	// call draws from it before it allocates. It sits next to the payload
	// budget (maxHTTPBody) and has no config knob.
	//
	// goja checks vm.Interrupt only between instructions, so a single
	// `"x".repeat(1e9)` runs to completion, deadline or not: the timeout cannot
	// stop it, only a refusal before the call can.
	maxScriptAllocBytes = 64 * 1024 * 1024

	// jsValueSlotBytes is what one array slot costs goja (a Value interface).
	jsValueSlotBytes = 16

	// joinCtxCheckEvery is how many elements join() handles between two
	// checks of the run's deadline: the Go loop is not reached by vm.Interrupt.
	joinCtxCheckEvery = 1 << 14

	// errMemoryBudgetPrefix starts every refusal message, so the check's
	// ERROR output says what happened.
	errMemoryBudgetPrefix = "memory budget exceeded"
)

// Typed-array constructors whose allocation is charged. SharedArrayBuffer is
// listed with ArrayBuffer and skipped when the goja build does not define it.
//
//nolint:gochecknoglobals // Fixed list of JS builtin names.
var typedArrayCtorNames = []string{
	"Int8Array", "Uint8Array", "Uint8ClampedArray",
	"Int16Array", "Uint16Array",
	"Int32Array", "Uint32Array",
	"Float32Array", "Float64Array",
	"BigInt64Array", "BigUint64Array",
}

//nolint:gochecknoglobals // Comparison target for a goja export type.
var arrayBufferType = reflect.TypeOf(goja.ArrayBuffer{})

// reflectTypeArray is what a goja array (dense or sparse) exports as.
//
//nolint:gochecknoglobals // Comparison target for a goja export type.
var reflectTypeArray = reflect.TypeOf([]any{})

// memoryBudget is the allocation state of one run. The VM is single-threaded
// and every charge happens on its goroutine, so it needs no lock.
type memoryBudget struct {
	used int64
	// rangeError is the RangeError constructor captured before the script
	// runs: a script that reassigns the global cannot dodge the refusal.
	rangeError goja.Constructor
	// joining holds the objects join() is walking, so a cyclic array joins
	// to "" instead of recursing forever (what goja's own join does).
	joining map[*goja.Object]struct{}
	// isArray is the original Array.isArray, captured before the script runs
	// (it sees through a Proxy, which isRealArray does not).
	isArray goja.Callable
	// regexpReplace is RegExp.prototype[Symbol.replace] as installed, so
	// replace() knows a regexp pattern from a script-defined replacer.
	regexpReplace goja.Value
	// guarded holds the binding objects whose methods already refuse a huge
	// unflattened string, so a handle returned twice is wrapped once.
	guarded map[*goja.Object]struct{}
}

// fits reports whether n more bytes stay within the budget.
func (r *jsRuntime) fits(n float64) bool {
	return n <= float64(maxScriptAllocBytes-r.memory.used)
}

// refuseAlloc throws the RangeError a refused allocation surfaces as. Nothing
// is allocated, so nothing is spent.
func (r *jsRuntime) refuseAlloc(what string, n float64) {
	msg := fmt.Sprintf(
		"%s: %s would allocate %.0f bytes; a JS check may allocate at most %d MiB in total (%d bytes left)",
		errMemoryBudgetPrefix, what, n, maxScriptAllocBytes>>20, maxScriptAllocBytes-r.memory.used)

	r.throwRangeError(msg)
}

// throwRangeError throws a RangeError built with the constructor captured
// before the script ran.
func (r *jsRuntime) throwRangeError(msg string) {
	if r.memory.rangeError != nil {
		if obj, err := r.memory.rangeError(nil, r.vm.ToValue(msg)); err == nil {
			panic(obj)
		}
	}

	panic(r.vm.NewGoError(fmt.Errorf("%s", msg))) //nolint:err113 // Built per call: carries the sizes.
}

// checkAlloc refuses a call that would need n bytes it does not have, without
// spending them.
func (r *jsRuntime) checkAlloc(what string, n float64) {
	if n > 0 && !r.fits(n) {
		r.refuseAlloc(what, n)
	}
}

// spendAlloc draws n bytes from the run's budget, or refuses the call.
func (r *jsRuntime) spendAlloc(what string, n float64) {
	if n <= 0 {
		return
	}

	r.checkAlloc(what, n)
	r.memory.used += int64(n)
}

// toIntegerOrInfinity is ECMAScript's ToIntegerOrInfinity: NaN is 0, the rest
// truncates toward zero and keeps infinities.
func toIntegerOrInfinity(v goja.Value) float64 {
	if v == nil || goja.IsUndefined(v) {
		return 0
	}

	f := v.ToNumber().ToFloat()
	if math.IsNaN(f) {
		return 0
	}

	return math.Trunc(f)
}

// toLength is ECMAScript's ToLength: an integer clamped to [0, 2^53-1].
func toLength(v goja.Value) float64 {
	const maxSafeInteger = 1<<53 - 1

	n := toIntegerOrInfinity(v)
	if n <= 0 {
		return 0
	}

	return math.Min(n, maxSafeInteger)
}

// relativeIndex resolves a relative start/end index against a length, as
// Array.prototype.fill does.
func relativeIndex(rel, length float64) float64 {
	if rel >= 0 {
		return math.Min(rel, length)
	}

	return math.Max(length+rel, 0)
}

// jsStringLen is a JS string's length in UTF-16 code units, the unit the
// budget counts in.
func jsStringLen(v goja.Value) int {
	if s, ok := v.(goja.String); ok {
		return s.Length()
	}

	return len(v.String())
}

// callOriginal runs a builtin's original implementation, rethrowing what it
// throws.
func callOriginal(orig goja.Callable, this goja.Value, args ...goja.Value) goja.Value {
	val, err := orig(this, args...)
	if err != nil {
		panic(err)
	}

	return val
}

// installMemoryBudget caps the native builtins that can build a large value
// in one call, on this VM's own intrinsics, before the script runs. It is a
// denylist: a pure-JS allocation loop is still only stopped by the timeout.
func (r *jsRuntime) installMemoryBudget() {
	r.memory.joining = map[*goja.Object]struct{}{}
	r.memory.guarded = map[*goja.Object]struct{}{}

	if ctor, ok := goja.AssertConstructor(r.vm.Get("RangeError")); ok {
		r.memory.rangeError = ctor
	}

	stringProto := r.vm.Get("String").ToObject(r.vm).Get("prototype").ToObject(r.vm)
	r.wrapMethod(stringProto, "repeat", 1, r.capRepeat)
	r.wrapMethod(stringProto, "padStart", 2, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capPad("String.prototype.padStart", orig)
	})
	r.wrapMethod(stringProto, "padEnd", 2, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capPad("String.prototype.padEnd", orig)
	})

	arrayCtor := r.vm.Get("Array").ToObject(r.vm)
	arrayProto := arrayCtor.Get("prototype").ToObject(r.vm)
	r.memory.isArray, _ = goja.AssertFunction(arrayCtor.Get("isArray"))
	r.setMethod(arrayProto, "join", 1, r.boundedJoin("Array.prototype.join"))
	r.wrapMethod(arrayProto, "fill", 1, r.capArrayFill)
	r.wrapMethod(arrayCtor, "from", 1, r.capArrayFrom)
	r.installArrayCopyCaps(arrayProto)
	r.replaceCtor("Array", r.chargeArrayCtor, false)

	r.replaceCtor("ArrayBuffer", r.chargeArrayBufferCtor, true)
	r.replaceCtor("SharedArrayBuffer", r.chargeArrayBufferCtor, true)

	for _, name := range typedArrayCtorNames {
		r.replaceCtor(name, r.chargeTypedArrayCtor(name), true)
	}

	// %TypedArray%.prototype.join is its own function: a 4 MiB Uint8Array
	// joined with a long separator would amplify just the same.
	if u8, ok := r.vm.Get("Uint8Array").(*goja.Object); ok {
		if typedProto := u8.Get("prototype").ToObject(r.vm).Prototype(); typedProto != nil {
			r.setMethod(typedProto, "join", 1, r.boundedJoin("TypedArray.prototype.join"))
		}
	}

	r.installStringCaps(stringProto)
	r.installArgListCaps()
}

// installArgListCaps caps the calls that build an argument list from an
// array-like: `Math.max.apply(null, {length: 2e8})` makes a 3 GB slice in
// one native call. A real array is passed as is (goja reuses its slots); an
// array-like is checked for its length, pinned so goja reads the same one.
// The list is transient, so it is checked, not spent.
func (r *jsRuntime) installArgListCaps() {
	if fnCtor, ok := r.vm.Get("Function").(*goja.Object); ok {
		if fnProto, ok := fnCtor.Get("prototype").(*goja.Object); ok {
			r.wrapMethod(fnProto, "apply", 2, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
				return r.capArgList("Function.prototype.apply", orig, 1)
			})
		}
	}

	if reflectObj, ok := r.vm.Get("Reflect").(*goja.Object); ok {
		r.wrapMethod(reflectObj, "apply", 3, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
			return r.capArgList("Reflect.apply", orig, 2)
		})
		r.wrapMethod(reflectObj, "construct", 2, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
			return r.capArgList("Reflect.construct", orig, 1)
		})
	}
}

// capArgList checks the array-like at argument position pos for the slice
// goja will build from it.
func (r *jsRuntime) capArgList(what string, orig goja.Callable, pos int) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		obj, ok := call.Argument(pos).(*goja.Object)
		if !ok || isRealArray(obj) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		snap, length := r.snapshotLength(obj)
		r.checkAlloc(what, length*jsValueSlotBytes)

		args := append([]goja.Value{}, call.Arguments...)
		args[pos] = snap

		return callOriginal(orig, call.This, args...)
	}
}

// setMethod defines fn as a non-enumerable method, the way builtins are.
func (r *jsRuntime) setMethod(
	obj *goja.Object, name string, length int, fn func(goja.FunctionCall) goja.Value,
) {
	_ = obj.DefineDataProperty(name, r.methodObject(name, length, fn), goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)
}

// wrapMethod replaces obj[name] with a guard built around the original.
func (r *jsRuntime) wrapMethod(
	obj *goja.Object, name string, length int,
	wrap func(orig goja.Callable) func(goja.FunctionCall) goja.Value,
) {
	orig, ok := goja.AssertFunction(obj.Get(name))
	if !ok {
		return
	}

	r.setMethod(obj, name, length, wrap(orig))
}

// capRepeat charges String.prototype.repeat for its whole result.
func (r *jsRuntime) capRepeat(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if goja.IsUndefined(call.This) || goja.IsNull(call.This) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		str := call.This.ToString()
		count := toIntegerOrInfinity(call.Argument(0))

		// A negative or infinite count is the original's RangeError to throw.
		if count > 0 && !math.IsInf(count, 1) {
			r.spendAlloc("String.prototype.repeat", float64(jsStringLen(str))*count)
		}

		// The converted values go in: a valueOf/toString runs once.
		return callOriginal(orig, str, r.vm.ToValue(count))
	}
}

// capPad charges padStart/padEnd for the padded result.
func (r *jsRuntime) capPad(what string, orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if goja.IsUndefined(call.This) || goja.IsNull(call.This) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		str := call.This.ToString()
		maxLength := toLength(call.Argument(0))

		fill := call.Argument(1)
		fillLen := 1 // the default filler is " "

		if !goja.IsUndefined(fill) {
			fill = fill.ToString()
			fillLen = jsStringLen(fill)
		}

		if fillLen > 0 && maxLength > float64(jsStringLen(str)) {
			r.spendAlloc(what, maxLength)
		}

		return callOriginal(orig, str, r.vm.ToValue(maxLength), fill)
	}
}

// boundedJoin is Array.prototype.join (and the typed-array one) rewritten in
// Go so every piece is charged BEFORE it is appended: goja's own join grows
// its builder in a Go loop the interrupt never reaches.
func (r *jsRuntime) boundedJoin(what string) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		obj := call.This.ToObject(r.vm)

		if _, cyclic := r.memory.joining[obj]; cyclic {
			return r.vm.ToValue("")
		}

		r.memory.joining[obj] = struct{}{}
		defer delete(r.memory.joining, obj)

		length := toLength(obj.Get("length"))

		sep := r.vm.ToValue(",")
		if arg := call.Argument(0); !goja.IsUndefined(arg) {
			sep = arg.ToString()
		}

		if length == 0 {
			return r.vm.ToValue("")
		}

		sepLen := float64(jsStringLen(sep))

		// Each element counts as at least one unit here (not spent): it bounds
		// the Go loop below for a sparse array of a few billion holes joined
		// with "".
		r.checkAlloc(what, length+sepLen*(length-1))
		r.spendAlloc(what, sepLen*(length-1))

		sepStr, _ := sep.(goja.String)

		var buf goja.StringBuilder

		for idx := int64(0); idx < int64(length); idx++ {
			if idx%joinCtxCheckEvery == 0 && idx > 0 {
				if err := r.execCtx.Err(); err != nil {
					panic(r.vm.NewGoError(err))
				}
			}

			if idx > 0 && sepLen > 0 {
				writeJSString(&buf, sepStr, sep)
			}

			elem := obj.GetIdx(idx)
			if elem == nil || goja.IsUndefined(elem) || goja.IsNull(elem) {
				continue
			}

			str := elem.ToString()
			r.spendAlloc(what, float64(jsStringLen(str)))

			strVal, _ := str.(goja.String)
			writeJSString(&buf, strVal, str)
		}

		return buf.String()
	}
}

// writeJSString appends a JS string, keeping UTF-16 intact when goja hands
// one over.
func writeJSString(buf *goja.StringBuilder, str goja.String, val goja.Value) {
	if str != nil {
		buf.WriteString(str)

		return
	}

	buf.WriteUTF8String(val.String())
}

// capArrayFill charges Array.prototype.fill for the slots it writes: on a
// sparse array of a billion holes, goja creates each one in a Go loop.
func (r *jsRuntime) capArrayFill(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		obj := call.This.ToObject(r.vm)
		length := toLength(obj.Get("length"))
		start := relativeIndex(toIntegerOrInfinity(call.Argument(1)), length)

		end := length
		if arg := call.Argument(2); !goja.IsUndefined(arg) {
			end = relativeIndex(toIntegerOrInfinity(arg), length)
		}

		if end > start {
			r.spendAlloc("Array.prototype.fill", (end-start)*jsValueSlotBytes)
		}

		return callOriginal(orig, obj, call.Argument(0), r.vm.ToValue(start), r.vm.ToValue(end))
	}
}

// capArrayFrom charges Array.from for an array-like's length: `{length: 1e9}`
// becomes a billion slots in one native call.
func (r *jsRuntime) capArrayFrom(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		args := call.Arguments
		src := call.Argument(0)

		// An array-like (no iterator) is read by its length: freeze that
		// length so a getter cannot answer 0 here and 4e9 to the original.
		if obj, ok := src.(*goja.Object); ok && !isRealArray(obj) && !hasIterator(obj) {
			snap, length := r.snapshotLength(obj)
			r.spendAlloc("Array.from", length*jsValueSlotBytes)

			args = append([]goja.Value{snap}, args[1:]...)

			return callOriginal(orig, call.This, args...)
		}

		r.spendAlloc("Array.from", r.lengthOf(src)*jsValueSlotBytes)

		return callOriginal(orig, call.This, args...)
	}
}

// arrayCopyCharge sizes a call of an Array.prototype method from its
// receiver's length: the slots it allocates, and the arguments to pass on
// (converted, so a valueOf runs once and cannot answer differently twice).
type arrayCopyCharge func(call goja.FunctionCall, length float64) (slots float64, args []goja.Value)

// installArrayCopyCaps caps the Array.prototype methods that allocate from
// the receiver's `length` in one native call. Setting `a.length = 4e9` costs
// nothing, but goja then preallocates from it: sort and toSorted
// `make([]Value, 0, length)` on a holey array or an array-like, map
// `make([]Value, length)`, and with, toReversed and toSpliced fill a new
// array slot by slot in a Go loop.
//
// The copies are spent (the result is new memory the script keeps); sort's
// buffer is transient, so sort is only checked against what is left.
//
// slice, splice, filter and concat copy every element of a DENSE array into a
// new one in one native call, so `a = a.concat(a)` doubles the live memory
// per call (spec 2026-11-01-01): they are charged for their result, 16 bytes
// per slot, sized from the length (filter keeps at most all of them). flat
// and flatMap are rewritten in Go (boundedFlat): their result is the sum of
// nested lengths goja would read through getters of its own.
func (r *jsRuntime) installArrayCopyCaps(arrayProto *goja.Object) {
	wholeLength := func(call goja.FunctionCall, length float64) (float64, []goja.Value) {
		return length, call.Arguments
	}

	r.wrapMethod(arrayProto, "sort", 1, r.capArrayCopy("Array.prototype.sort", false, wholeLength))
	r.wrapMethod(arrayProto, "map", 1, r.capArrayCopy("Array.prototype.map", true, wholeLength))
	r.wrapMethod(arrayProto, "filter", 1, r.capArrayCopy("Array.prototype.filter", true, wholeLength))
	r.wrapMethod(arrayProto, "toReversed", 0, r.capArrayCopy("Array.prototype.toReversed", true, wholeLength))
	r.wrapMethod(arrayProto, "toSorted", 1, r.capArrayCopy("Array.prototype.toSorted", true,
		func(call goja.FunctionCall, length float64) (float64, []goja.Value) {
			if length >= math.MaxUint32 {
				return 0, call.Arguments // the original's RangeError
			}

			return length, call.Arguments
		}))
	r.wrapMethod(arrayProto, "with", 2, r.capArrayCopy("Array.prototype.with", true,
		func(call goja.FunctionCall, length float64) (float64, []goja.Value) {
			return length, []goja.Value{r.vm.ToValue(toIntegerOrInfinity(call.Argument(0))), call.Argument(1)}
		}))
	r.wrapMethod(arrayProto, "toSpliced", 2, r.capArrayCopy("Array.prototype.toSpliced", true, r.toSplicedCharge))
	r.wrapMethod(arrayProto, "slice", 2, r.capArrayCopy("Array.prototype.slice", true, r.sliceCharge))
	r.wrapMethod(arrayProto, "splice", 2, r.capArrayCopy("Array.prototype.splice", true, r.spliceCharge))
	r.wrapMethod(arrayProto, "concat", 1, r.capConcat)
	r.setMethod(arrayProto, "flat", 0, r.boundedFlat("Array.prototype.flat", false))
	r.setMethod(arrayProto, "flatMap", 1, r.boundedFlat("Array.prototype.flatMap", true))
}

// sliceCharge sizes slice's result: the elements between start and end.
func (r *jsRuntime) sliceCharge(call goja.FunctionCall, length float64) (float64, []goja.Value) {
	start := relativeIndex(toIntegerOrInfinity(call.Argument(0)), length)

	end := length
	if arg := call.Argument(1); !goja.IsUndefined(arg) {
		end = relativeIndex(toIntegerOrInfinity(arg), length)
	}

	return math.Max(end-start, 0), []goja.Value{r.vm.ToValue(start), r.vm.ToValue(end)}
}

// spliceCharge sizes splice's result (the removed elements) plus the slots
// the inserted items add to the receiver.
func (r *jsRuntime) spliceCharge(call goja.FunctionCall, length float64) (float64, []goja.Value) {
	if len(call.Arguments) == 0 {
		return 0, nil
	}

	start := relativeIndex(toIntegerOrInfinity(call.Argument(0)), length)
	args := []goja.Value{r.vm.ToValue(start)}
	skip := length - start

	if len(call.Arguments) > 1 {
		skip = math.Min(math.Max(toIntegerOrInfinity(call.Argument(1)), 0), length-start)
		args = append(args, r.vm.ToValue(skip))
		args = append(args, call.Arguments[2:]...)
	}

	items := math.Max(float64(len(call.Arguments)-2), 0)

	return skip + items, args
}

// capConcat charges Array.prototype.concat for its result: the length of
// every spreadable item (the receiver included), one slot for the others.
// Each item is pinned to what was charged: a spreadable array-like goes
// behind a Proxy that answers the length and the spreadability read here, a
// non-spreadable object goes inside a one-element array that is always
// spread, so a getter cannot flip either answer between the charge and goja.
func (r *jsRuntime) capConcat(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		this := call.This.ToObject(r.vm) // undefined/null: the original's TypeError
		items := make([]goja.Value, 0, len(call.Arguments)+1)
		slots := 0.0

		for _, item := range append([]goja.Value{this}, call.Arguments...) {
			pinned, n := r.pinConcatItem(item)
			items = append(items, pinned)
			slots += n
		}

		r.spendAlloc("Array.prototype.concat", slots*jsValueSlotBytes)

		return callOriginal(orig, items[0], items[1:]...)
	}
}

// pinConcatItem returns the value concat should see for item, and the slots
// it adds to the result.
func (r *jsRuntime) pinConcatItem(item goja.Value) (goja.Value, float64) {
	obj, ok := item.(*goja.Object)
	if !ok {
		return item, 1
	}

	// A real array's length is a plain value: spread or not, it adds at
	// most that many slots.
	if isRealArray(obj) {
		return obj, math.Max(toLength(obj.Get("length")), 1)
	}

	if !r.isConcatSpreadable(obj) {
		box := r.vm.NewArray(obj)
		_ = box.DefineDataPropertySymbol(goja.SymIsConcatSpreadable, r.vm.ToValue(true),
			goja.FLAG_FALSE, goja.FLAG_FALSE, goja.FLAG_FALSE)

		return box, 1
	}

	length := toLength(obj.Get("length"))

	return r.pinLength(obj, length, true), length
}

// isConcatSpreadable is ECMAScript's IsConcatSpreadable, read once.
func (r *jsRuntime) isConcatSpreadable(obj *goja.Object) bool {
	if v := obj.GetSymbol(goja.SymIsConcatSpreadable); v != nil && !goja.IsUndefined(v) {
		return v.ToBoolean()
	}

	return r.isArrayValue(obj)
}

// isArrayValue is Array.isArray: true for an array or a Proxy of one.
func (r *jsRuntime) isArrayValue(obj *goja.Object) bool {
	if isRealArray(obj) {
		return true
	}

	if r.memory.isArray == nil {
		return false
	}

	return callOriginal(r.memory.isArray, goja.Undefined(), obj).ToBoolean()
}

// maxFlatNesting bounds how many nested arrays flat() descends into, the
// way an engine's stack does: `a = [a]` built 1e6 times deep would
// otherwise recurse in Go as deep as the script asks.
const maxFlatNesting = 1000

// boundedFlat is Array.prototype.flat (and flatMap) rewritten in Go, so every
// element is charged BEFORE it is appended: goja's flattenIntoArray reads
// each nested array's length through the script's own getters and copies it
// in a Go loop the interrupt never reaches. The result is a plain Array
// (species is not honored), dense as the spec makes it.
func (r *jsRuntime) boundedFlat(what string, mapper bool) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		src := call.This.ToObject(r.vm)
		length := toLength(src.Get("length"))
		depth := 1.0

		var (
			mapFn   goja.Callable
			thisArg = goja.Undefined()
		)

		if mapper {
			fn, ok := goja.AssertFunction(call.Argument(0))
			if !ok {
				panic(r.vm.NewTypeError("flatMap mapper function is not callable"))
			}

			mapFn = fn

			if len(call.Arguments) > 1 {
				thisArg = call.Argument(1)
			}
		} else if arg := call.Argument(0); !goja.IsUndefined(arg) {
			depth = toIntegerOrInfinity(arg)
		}

		flat := &flattener{r: r, what: what}
		flat.into(src, length, depth, mapFn, thisArg, 0)

		out := make([]any, len(flat.out))
		for i, v := range flat.out {
			out[i] = v
		}

		return r.vm.NewArray(out...)
	}
}

// flattener collects flat()'s result, charging each element as it goes.
type flattener struct {
	r       *jsRuntime
	what    string
	out     []goja.Value
	visited int64
}

func (f *flattener) into(
	src *goja.Object, length, depth float64, mapFn goja.Callable, thisArg goja.Value, nesting int,
) {
	if nesting > maxFlatNesting {
		f.r.throwRangeError(f.what + ": Maximum call stack size exceeded")
	}

	for idx := int64(0); float64(idx) < length; idx++ {
		f.visited++
		if f.visited%joinCtxCheckEvery == 0 {
			if err := f.r.execCtx.Err(); err != nil {
				panic(f.r.vm.NewGoError(err))
			}
		}

		elem := src.GetIdx(idx)
		if elem == nil {
			continue // a hole
		}

		if mapFn != nil {
			elem = callOriginal(mapFn, thisArg, elem, f.r.vm.ToValue(idx), src)
		}

		if depth > 0 {
			if obj, ok := elem.(*goja.Object); ok && f.r.isArrayValue(obj) {
				f.into(obj, toLength(obj.Get("length")), depth-1, nil, nil, nesting+1)

				continue
			}
		}

		f.r.spendAlloc(f.what, jsValueSlotBytes)
		f.out = append(f.out, elem)
	}
}

// toSplicedCharge sizes toSpliced's result as goja does: length minus the
// skipped elements plus the inserted ones.
func (r *jsRuntime) toSplicedCharge(call goja.FunctionCall, length float64) (float64, []goja.Value) {
	if len(call.Arguments) == 0 {
		return length, nil
	}

	start := relativeIndex(toIntegerOrInfinity(call.Argument(0)), length)
	args := []goja.Value{r.vm.ToValue(start)}
	skip := length - start

	if len(call.Arguments) > 1 {
		skip = math.Min(math.Max(toIntegerOrInfinity(call.Argument(1)), 0), length-start)
		args = append(args, r.vm.ToValue(skip))
		args = append(args, call.Arguments[2:]...)
	}

	items := math.Max(float64(len(call.Arguments)-2), 0)

	return length - skip + items, args
}

// capArrayCopy charges an Array.prototype method for the slots it allocates
// before it runs. spend draws them from the budget; otherwise the call is
// only refused when they do not fit (a transient buffer).
func (r *jsRuntime) capArrayCopy(
	what string, spend bool, charge arrayCopyCharge,
) func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return func(call goja.FunctionCall) goja.Value {
			this := call.This.ToObject(r.vm) // undefined/null: the original's TypeError
			snap, length := r.snapshotLength(this)
			slots, args := charge(call, length)

			if spend {
				r.spendAlloc(what, slots*jsValueSlotBytes)
			} else {
				r.checkAlloc(what, slots*jsValueSlotBytes)
			}

			result := callOriginal(orig, snap, args...)

			// sort returns its receiver: the script's object, not the snapshot.
			if obj, ok := result.(*goja.Object); ok && obj.SameAs(snap) {
				return this
			}

			return result
		}
	}
}

// isRealArray reports whether obj is a goja array (or a wrapped Go slice):
// its length is a plain value, read the same by every caller.
func isRealArray(obj *goja.Object) bool {
	return obj.ExportType() == reflectTypeArray
}

// hasIterator reports whether obj has a Symbol.iterator method, so Array.from
// iterates it instead of reading its length.
func hasIterator(obj *goja.Object) bool {
	it := obj.GetSymbol(goja.SymIterator)

	return it != nil && !goja.IsUndefined(it) && !goja.IsNull(it)
}

// snapshotLength reads obj.length once and returns the object the original
// builtin should run on, with that length. A real array goes as is; anything
// else goes behind a Proxy that answers the length read here, so a getter
// cannot report a small length to the charge and a huge one to goja.
func (r *jsRuntime) snapshotLength(obj *goja.Object) (*goja.Object, float64) {
	if isRealArray(obj) {
		return obj, toLength(obj.Get("length"))
	}

	length := toLength(obj.Get("length"))

	return r.pinLength(obj, length, false), length
}

// pinLength puts obj behind a Proxy whose `length` is length, and, for
// concat, whose Symbol.isConcatSpreadable is true.
func (r *jsRuntime) pinLength(obj *goja.Object, length float64, spreadable bool) *goja.Object {
	lengthVal := r.vm.ToValue(length)

	traps := &goja.ProxyTrapConfig{
		Get: func(target *goja.Object, property string, _ goja.Value) goja.Value {
			if property == "length" {
				return lengthVal
			}

			if v := target.Get(property); v != nil {
				return v
			}

			return goja.Undefined()
		},
	}

	if spreadable {
		trueVal := r.vm.ToValue(true)
		traps.GetSym = func(target *goja.Object, property *goja.Symbol, _ goja.Value) goja.Value {
			if property == goja.SymIsConcatSpreadable {
				return trueVal
			}

			if v := target.GetSymbol(property); v != nil {
				return v
			}

			return goja.Undefined()
		}
	}

	return r.vm.ToValue(r.vm.NewProxy(obj, traps)).ToObject(r.vm)
}

// lengthOf is the `length` of an object or string source, 0 for anything else.
func (r *jsRuntime) lengthOf(src goja.Value) float64 {
	if src == nil || goja.IsUndefined(src) || goja.IsNull(src) {
		return 0
	}

	if str, ok := src.(goja.String); ok {
		return float64(str.Length())
	}

	obj, ok := src.(*goja.Object)
	if !ok {
		return 0
	}

	return toLength(obj.Get("length"))
}

// chargeFunc charges a constructor call and returns the arguments to pass on
// (converted, so a valueOf runs once).
type chargeFunc func(target *goja.Object, args []goja.Value) []goja.Value

// replaceCtor swaps a global constructor for a Proxy that charges each call.
// For allocating constructors (fixPrototype), prototype.constructor points
// at the Proxy too: `new (new ArrayBuffer(0).constructor)(n)` and the species
// paths (`slice`, `map`, `subarray`) go through the charge. Array keeps its
// own: `new Array(n)` allocates nothing in goja, and every Array method
// would otherwise cross the Proxy through its species constructor.
func (r *jsRuntime) replaceCtor(name string, charge chargeFunc, fixPrototype bool) {
	target, ok := r.vm.Get(name).(*goja.Object)
	if !ok {
		return
	}

	ctor, ok := goja.AssertConstructor(target)
	if !ok {
		return
	}

	call, _ := goja.AssertFunction(target)

	proxy := r.vm.NewProxy(target, &goja.ProxyTrapConfig{
		Construct: func(target *goja.Object, args []goja.Value, newTarget *goja.Object) *goja.Object {
			args = charge(target, args)

			obj, err := ctor(newTarget, args...)
			if err != nil {
				panic(err)
			}

			return obj
		},
		Apply: func(target *goja.Object, this goja.Value, args []goja.Value) goja.Value {
			if fixPrototype {
				// ArrayBuffer and typed arrays require `new`: the original throws.
				return callOriginal(call, this, args...)
			}

			return callOriginal(call, this, charge(target, args)...)
		},
	})

	// goja's Proxy has no [[HasInstance]] of its own, so `x instanceof
	// Uint8Array` would throw: the target answers through the Proxy instead.
	_ = target.DefineDataPropertySymbol(goja.SymHasInstance, r.vm.ToValue(r.ordinaryHasInstance),
		goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)

	proxyObj := r.vm.ToValue(proxy).ToObject(r.vm)
	_ = r.vm.GlobalObject().DefineDataProperty(name, proxyObj, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)

	if fixPrototype {
		if proto, ok := target.Get("prototype").(*goja.Object); ok {
			_ = proto.DefineDataProperty("constructor", proxyObj, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)
		}
	}
}

// ordinaryHasInstance is ECMAScript's OrdinaryHasInstance on `this`, the
// constructor (the Proxy, or a subclass of it) the instanceof is about.
func (r *jsRuntime) ordinaryHasInstance(call goja.FunctionCall) goja.Value {
	obj, ok := call.Argument(0).(*goja.Object)
	if !ok {
		return r.vm.ToValue(false)
	}

	proto, ok := call.This.ToObject(r.vm).Get("prototype").(*goja.Object)
	if !ok {
		panic(r.vm.NewTypeError("Function has non-object prototype in instanceof check"))
	}

	for p := obj.Prototype(); p != nil; p = p.Prototype() {
		if p.SameAs(proto) {
			return r.vm.ToValue(true)
		}
	}

	return r.vm.ToValue(false)
}

// chargeArrayCtor charges `Array(n)` / `new Array(n)` for n slots.
func (r *jsRuntime) chargeArrayCtor(_ *goja.Object, args []goja.Value) []goja.Value {
	if len(args) != 1 {
		return args
	}

	if _, isObj := args[0].(*goja.Object); isObj {
		return args
	}

	if _, isStr := args[0].(goja.String); isStr {
		return args
	}

	n := args[0].ToFloat()
	// A non-integer or out-of-range length is the original's RangeError.
	if n > 0 && n == math.Trunc(n) && n <= math.MaxUint32 {
		r.spendAlloc("Array(length)", n*jsValueSlotBytes)
	}

	return args
}

// chargeArrayBufferCtor charges `new ArrayBuffer(n)` for its n bytes.
func (r *jsRuntime) chargeArrayBufferCtor(target *goja.Object, args []goja.Value) []goja.Value {
	if len(args) == 0 {
		return args
	}

	n := toIntegerOrInfinity(args[0])
	if n < 0 || math.IsInf(n, 1) {
		return args // the original's RangeError
	}

	r.spendAlloc("new "+ctorName(target, "ArrayBuffer")+"(length)", n)

	out := append([]goja.Value{r.vm.ToValue(n)}, args[1:]...)

	return out
}

// chargeTypedArrayCtor charges a typed-array constructor for the bytes it
// allocates: length × BYTES_PER_ELEMENT for a length, an array-like or a
// typed array source; nothing for a view on an existing buffer.
func (r *jsRuntime) chargeTypedArrayCtor(name string) chargeFunc {
	return func(target *goja.Object, args []goja.Value) []goja.Value {
		if len(args) == 0 {
			return args
		}

		bytesPerElement := toIntegerOrInfinity(target.Get("BYTES_PER_ELEMENT"))
		what := "new " + name

		if obj, isObj := args[0].(*goja.Object); isObj {
			if obj.ExportType() == arrayBufferType {
				return args // a view: no new memory
			}

			// A typed array source is copied by its internal length, which
			// no `length` override on the object changes.
			if n, ok := typedArrayLength(obj); ok {
				r.spendAlloc(what+"(source)", n*bytesPerElement)

				return args
			}

			// An array-like is read by its length: goja reads it again, so
			// it gets a Proxy answering the length charged here (a getter
			// cannot answer 0 to the charge and 4e9 to the allocation).
			if !isRealArray(obj) && !hasIterator(obj) {
				snap, length := r.snapshotLength(obj)
				r.spendAlloc(what+"(source)", length*bytesPerElement)

				return append([]goja.Value{snap}, args[1:]...)
			}

			// An array's length is a plain value; an iterable is walked by
			// its iterator, one value at a time.
			r.spendAlloc(what+"(source)", r.lengthOf(obj)*bytesPerElement)

			return args
		}

		if goja.IsUndefined(args[0]) || goja.IsNull(args[0]) {
			return args
		}

		n := toIntegerOrInfinity(args[0])
		if n < 0 || math.IsInf(n, 1) {
			return args // the original's RangeError
		}

		r.spendAlloc(what+"(length)", n*bytesPerElement)

		return append([]goja.Value{r.vm.ToValue(n)}, args[1:]...)
	}
}

// typedArrayLength is a typed array's (or a wrapped Go slice's) element
// count, read from the Go side rather than from its `length` property.
func typedArrayLength(obj *goja.Object) (float64, bool) {
	typ := obj.ExportType()
	if typ == nil || typ.Kind() != reflect.Slice || typ == reflectTypeArray {
		return 0, false
	}

	return float64(reflect.ValueOf(obj.Export()).Len()), true
}

// ctorName is the constructor's own name, for the refusal message.
func ctorName(target *goja.Object, fallback string) string {
	if name := target.Get("name"); name != nil && !goja.IsUndefined(name) {
		return name.String()
	}

	return fallback
}
