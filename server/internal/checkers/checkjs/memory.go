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
	r.setMethod(arrayProto, "join", 1, r.boundedJoin("Array.prototype.join"))
	r.wrapMethod(arrayProto, "fill", 1, r.capArrayFill)
	r.wrapMethod(arrayCtor, "from", 1, r.capArrayFrom)
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
}

// setMethod defines fn as a non-enumerable method, the way builtins are.
func (r *jsRuntime) setMethod(
	obj *goja.Object, name string, length int, fn func(goja.FunctionCall) goja.Value,
) {
	fnObj := r.vm.ToValue(fn).ToObject(r.vm)
	_ = fnObj.DefineDataProperty("name", r.vm.ToValue(name), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	_ = fnObj.DefineDataProperty("length", r.vm.ToValue(length), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	_ = obj.DefineDataProperty(name, fnObj, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)
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
		r.spendAlloc("Array.from", r.lengthOf(call.Argument(0))*jsValueSlotBytes)

		return callOriginal(orig, call.This, call.Arguments...)
	}
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

// ctorName is the constructor's own name, for the refusal message.
func ctorName(target *goja.Object, fallback string) string {
	if name := target.Get("name"); name != nil && !goja.IsUndefined(name) {
		return name.String()
	}

	return fallback
}
