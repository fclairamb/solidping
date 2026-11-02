package checkjs

import (
	"fmt"
	"math"
	"reflect"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dop251/goja"
	"golang.org/x/text/unicode/norm"
)

// Spec 2026-11-02-01: the builtins below amplify an input the budget
// accepted into an output it would not, in one native call.

const (
	// jsonGapMax is the longest indent JSON.stringify keeps.
	jsonGapMax = 10
	// The longest UTF-16 output one UTF-16 unit normalizes to, measured on
	// golang.org/x/text/unicode/norm (U+FB2C for NFC, U+1F82 for NFD,
	// U+FDFA for NFKC and NFKD). NFC recomposes what NFD expands, so it is
	// bounded by NFD's factor.
	normalizeMaxFactor  = 4
	normalizeKMaxFactor = 18
	// encodeURI turns one BMP unit into three %XX escapes; escape turns one
	// into %uXXXX.
	encodeURIMaxFactor = 9
	escapeMaxFactor    = 6
	// undefinedLen is what String.raw writes for a missing raw element.
	undefinedLen = len("undefined")
)

// The goja class names the wrappers below tell apart.
const (
	classNumber  = "Number"
	classString  = "String"
	classRegExp  = "RegExp"
	classBoolean = "Boolean"
	classError   = "Error"
	classRawJSON = "RawJSON"
)

// asciiStringTypeName is goja's ASCII-only string, whose String() is free.
const asciiStringTypeName = "goja.asciiString"

// installBuiltinCaps charges the amplifying builtins that are not a
// String.prototype or Array.prototype copy: JSON.stringify, toLocaleString,
// normalize, String.raw and the URI encoders.
func (r *jsRuntime) installBuiltinCaps(stringProto, arrayProto *goja.Object) {
	if jsonObj, ok := r.vm.Get("JSON").(*goja.Object); ok {
		r.wrapMethod(jsonObj, "stringify", 3, r.capJSONStringify)
	}

	r.setMethod(arrayProto, "toLocaleString", 0, r.boundedLocaleJoin("Array.prototype.toLocaleString", nil))

	if u8, ok := r.vm.Get("Uint8Array").(*goja.Object); ok {
		if typedProto := u8.Get("prototype").ToObject(r.vm).Prototype(); typedProto != nil {
			r.wrapMethod(typedProto, "toLocaleString", 0, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
				return r.boundedLocaleJoin("TypedArray.prototype.toLocaleString", orig)
			})
		}
	}

	r.wrapMethod(stringProto, "normalize", 0, r.capNormalize)

	if stringCtor, ok := r.vm.Get("String").(*goja.Object); ok {
		r.setMethod(stringCtor, "raw", 1, r.boundedStringRaw)
	}

	global := r.vm.GlobalObject()
	r.wrapMethod(global, "encodeURIComponent", 1, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capExpansion("encodeURIComponent", orig, encodeURIMaxFactor, uriEncodedLen(uriComponentUnescaped))
	})
	r.wrapMethod(global, "encodeURI", 1, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capExpansion("encodeURI", orig, encodeURIMaxFactor, uriEncodedLen(uriUnescaped))
	})
	r.wrapMethod(global, "escape", 1, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capExpansion("escape", orig, escapeMaxFactor, escapedLen)
	})
}

// installFlattenGuards puts the flatten guard in front of the calls outside
// String.prototype and RegExp.prototype that read a whole string argument:
// JSON.parse, the global parse/decode/encode functions, and RegExp, String
// and Number called as functions or constructors.
func (r *jsRuntime) installFlattenGuards() {
	if jsonObj, ok := r.vm.Get("JSON").(*goja.Object); ok {
		r.guardMethod(jsonObj, "JSON.parse", "parse")
	}

	global := r.vm.GlobalObject()
	for _, name := range []string{
		"parseInt", "parseFloat", "decodeURI", "decodeURIComponent",
		"encodeURI", "encodeURIComponent", "escape", "unescape",
	} {
		r.guardMethod(global, name, name)
	}

	for _, name := range []string{classRegExp, classString, classNumber} {
		what := name

		r.replaceCtor(name, func(_ *goja.Object, args []goja.Value) []goja.Value {
			for _, arg := range args {
				r.spendFlatten(what, arg)
			}

			return args
		}, false)
	}
}

// checkDeadline stops a Go loop the interrupt does not reach once the run's
// deadline has passed.
func (r *jsRuntime) checkDeadline(iteration int64) {
	if iteration%joinCtxCheckEvery != 0 || iteration == 0 {
		return
	}

	if err := r.execCtx.Err(); err != nil {
		panic(r.vm.NewGoError(err))
	}
}

// capExpansion charges a string function whose output is at most factor
// times its input. When that bound fits, the call runs and its real output is
// spent; when it does not, the exact output length is counted first (exact
// stops counting past what is left) and the call is refused if it does not
// fit.
func (r *jsRuntime) capExpansion(
	what string, orig goja.Callable, factor float64, exact func(goja.String, float64) float64,
) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		arg := call.Argument(0)
		if _, isSym := arg.(*goja.Symbol); isSym {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		str := arg.ToString()
		r.spendFlatten(what, str)

		strVal, ok := str.(goja.String)
		if ok && !r.fits(float64(strVal.Length())*factor) {
			left := float64(maxScriptAllocBytes - r.memory.used)
			r.checkAlloc(what, exact(strVal, left))
		}

		// The converted value goes in: a toString runs once.
		result := callOriginal(orig, call.This, str)
		r.spendAlloc(what, float64(jsStringLen(result)))

		return result
	}
}

// capNormalize charges String.prototype.normalize the way capExpansion
// charges a global function.
func (r *jsRuntime) capNormalize(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if goja.IsUndefined(call.This) || goja.IsNull(call.This) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		str := call.This.ToString()

		formArg := call.Argument(0)
		form := "NFC"

		if !goja.IsUndefined(formArg) {
			formArg = formArg.ToString()
			form = formArg.String()
		}

		normForm, factor, known := normalizationForm(form)
		if !known {
			return callOriginal(orig, str, formArg) // the original's RangeError
		}

		const what = "String.prototype.normalize"

		r.spendFlatten(what, str)

		if strVal, ok := str.(goja.String); ok && !r.fits(float64(strVal.Length())*factor) {
			left := float64(maxScriptAllocBytes - r.memory.used)
			r.checkAlloc(what, r.normalizedLen(normForm, strVal, left))
		}

		result := callOriginal(orig, str, formArg)
		r.spendAlloc(what, float64(jsStringLen(result)))

		return result
	}
}

// normalizationForm maps a normalize() form name to its norm.Form and its
// expansion bound.
func normalizationForm(name string) (norm.Form, float64, bool) {
	switch name {
	case "NFC":
		return norm.NFC, normalizeMaxFactor, true
	case "NFD":
		return norm.NFD, normalizeMaxFactor, true
	case "NFKC":
		return norm.NFKC, normalizeKMaxFactor, true
	case "NFKD":
		return norm.NFKD, normalizeKMaxFactor, true
	default:
		return norm.NFC, 0, false
	}
}

// normalizedLen bounds the UTF-16 length of str normalized to form without
// building it: each character counts for its full decomposition (canonical
// for NFC and NFD, compatibility for NFKC and NFKD), which composing only
// shortens. It stops once the count passes limit.
func (r *jsRuntime) normalizedLen(form norm.Form, str goja.String, limit float64) float64 {
	decompose := norm.NFD
	if form == norm.NFKC || form == norm.NFKD {
		decompose = norm.NFKD
	}

	text := str.String()
	count := float64(0)

	for pos, iteration := 0, int64(1); pos < len(text) && count <= limit; iteration++ {
		r.checkDeadline(iteration)

		props := decompose.PropertiesString(text[pos:])

		size := props.Size()
		if size == 0 {
			size = 1
		}

		char, _ := utf8.DecodeRuneInString(text[pos:])

		switch decomposition := props.Decomposition(); {
		case decomposition != nil:
			count += float64(utf16Len(string(decomposition)))
		case char >= hangulFirst && char <= hangulLast:
			count += hangulMaxJamo
		default:
			count += float64(utf16.RuneLen(char))
		}

		pos += size
	}

	return count
}

// Hangul syllables decompose algorithmically (Properties reports no
// decomposition for them), into at most three jamo.
const (
	hangulFirst   = 0xAC00
	hangulLast    = 0xD7A3
	hangulMaxJamo = 3
)

// utf16Len is the UTF-16 length of a Go string.
func utf16Len(text string) int {
	count := 0
	for _, char := range text {
		count += utf16.RuneLen(char)
	}

	return count
}

// uriComponentUnescaped and uriUnescaped are the ASCII characters
// encodeURIComponent and encodeURI keep as is.
const (
	uriComponentUnescaped = "-_.!~*'()"
	uriUnescaped          = uriComponentUnescaped + ";/?:@&=+$,#"
)

// uriEncodedLen counts what encodeURI (or encodeURIComponent) writes for str,
// up to limit. A lone surrogate is the original's URIError: counted as one
// escape.
func uriEncodedLen(keep string) func(goja.String, float64) float64 {
	var keepSet [128]bool
	for idx := range len(keep) {
		keepSet[keep[idx]] = true
	}

	return func(str goja.String, limit float64) float64 {
		count := float64(0)
		length := str.Length()

		for idx := 0; idx < length && count <= limit; idx++ {
			unit := str.CharAt(idx)

			switch {
			case unit < 0x80 && (isASCIIAlnum(unit) || keepSet[unit]):
				count++
			case unit < 0x80:
				count += 3
			case unit < 0x800:
				count += 6
			case utf16.IsSurrogate(rune(unit)) && idx+1 < length && utf16.IsSurrogate(rune(str.CharAt(idx+1))):
				count += 12
				idx++
			default:
				count += 9
			}
		}

		return count
	}
}

// escapedLen counts what escape() writes for str, up to limit.
func escapedLen(str goja.String, limit float64) float64 {
	count := float64(0)
	length := str.Length()

	for idx := 0; idx < length && count <= limit; idx++ {
		unit := str.CharAt(idx)

		switch {
		case unit < 0x80 && (isASCIIAlnum(unit) || unit == '@' || unit == '*' || unit == '_' ||
			unit == '+' || unit == '-' || unit == '.' || unit == '/'):
			count++
		case unit < 0x100:
			count += 3
		default:
			count += escapeMaxFactor
		}
	}

	return count
}

func isASCIIAlnum(unit uint16) bool {
	return unit >= 'A' && unit <= 'Z' || unit >= 'a' && unit <= 'z' || unit >= '0' && unit <= '9'
}

// boundedStringRaw is String.raw rewritten in Go so every segment and
// substitution is charged before it is appended: an array-like `raw` with a
// length of a billion writes "undefined" a billion times in one call.
func (r *jsRuntime) boundedStringRaw(call goja.FunctionCall) goja.Value {
	const what = "String.raw"

	cooked := call.Argument(0).ToObject(r.vm)

	rawVal := cooked.Get("raw")
	if rawVal == nil {
		rawVal = goja.Undefined()
	}

	raw := rawVal.ToObject(r.vm)
	segments := int64(toLength(raw.Get("length")))

	if segments <= 0 {
		return r.vm.ToValue("")
	}

	substitutions := int64(len(call.Arguments) - 1)

	var buf goja.StringBuilder

	for idx := int64(0); ; idx++ {
		r.checkDeadline(idx)

		r.appendCharged(&buf, what, raw.GetIdx(idx))

		if idx+1 == segments {
			return buf.String()
		}

		if idx < substitutions {
			r.appendCharged(&buf, what, call.Arguments[idx+1])
		}
	}
}

// appendCharged converts val to a string, charges it, and appends it.
func (r *jsRuntime) appendCharged(buf *goja.StringBuilder, what string, val goja.Value) {
	if val == nil || goja.IsUndefined(val) {
		r.spendAlloc(what, float64(undefinedLen))
		buf.WriteUTF8String("undefined")

		return
	}

	str := val.ToString()
	r.spendFlatten(what, str)
	r.spendAlloc(what, float64(jsStringLen(str)))

	strVal, _ := str.(goja.String)
	writeJSString(buf, strVal, str)
}

// boundedLocaleJoin is toLocaleString (Array's, or the typed-array one when
// orig is set) rewritten like boundedJoin: each element's toLocaleString()
// is charged before it is appended.
func (r *jsRuntime) boundedLocaleJoin(what string, orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		obj := call.This.ToObject(r.vm)

		if orig != nil {
			if _, typed := typedArrayLength(obj); !typed {
				return callOriginal(orig, call.This, call.Arguments...) // the original's TypeError
			}
		}

		if _, cyclic := r.memory.joining[obj]; cyclic {
			return r.vm.ToValue("")
		}

		r.memory.joining[obj] = struct{}{}
		defer delete(r.memory.joining, obj)

		length := toLength(obj.Get("length"))
		if length == 0 {
			return r.vm.ToValue("")
		}

		// As in boundedJoin: one unit per element bounds the loop, the
		// separators are spent.
		r.checkAlloc(what, 2*length-1)
		r.spendAlloc(what, length-1)

		var buf goja.StringBuilder

		for idx := int64(0); idx < int64(length); idx++ {
			r.checkDeadline(idx)

			if idx > 0 {
				buf.WriteUTF8String(",")
			}

			elem := obj.GetIdx(idx)
			if elem == nil || goja.IsUndefined(elem) || goja.IsNull(elem) {
				continue
			}

			method, ok := goja.AssertFunction(elem.ToObject(r.vm).Get("toLocaleString"))
			if !ok {
				panic(r.vm.NewTypeError(fmt.Sprintf("Property 'toLocaleString' of object %s is not a function", elem)))
			}

			r.appendCharged(&buf, what, callOriginal(method, elem))
		}

		return buf.String()
	}
}

// capJSONStringify runs JSON.stringify with a replacer of its own that
// charges every value goja is about to write (its key, its indentation, its
// characters), so the output never grows past the budget: an array of a
// million references to one string, or a shared nested array with an indent,
// is refused as it is written instead of after. A replacer function the
// script passed runs inside it, once per value, and a property list is
// served by a Proxy that lists the same keys in the same order.
func (r *jsRuntime) capJSONStringify(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		charge := &jsonCharge{r: r, levels: map[*goja.Object]jsonLevel{}}

		if replacer, ok := call.Argument(1).(*goja.Object); ok {
			if r.isArrayObject(replacer) {
				charge.keys = propertyList(replacer)
				charge.proxies = map[*goja.Object]*goja.Object{}
			} else if fn, isFn := goja.AssertFunction(replacer); isFn {
				charge.user = fn
			}
		}

		space := jsonSpace(call.Argument(2))
		charge.gap = jsonGapLen(space)

		return callOriginal(orig, call.This, call.Argument(0), r.vm.ToValue(charge.replace), space)
	}
}

// isArrayObject is Array.isArray as captured before the script ran.
func (r *jsRuntime) isArrayObject(obj *goja.Object) bool {
	if r.memory.isArray == nil {
		return isRealArray(obj)
	}

	return callOriginal(r.memory.isArray, goja.Undefined(), obj).ToBoolean()
}

// propertyList is JSON.stringify's property list from an array replacer:
// its strings and numbers (or their wrappers), deduplicated, in order.
func propertyList(replacer *goja.Object) []goja.Value {
	length := int64(toLength(replacer.Get("length")))
	seen := map[string]struct{}{}

	var keys []goja.Value

	for idx := range length {
		item := replacer.GetIdx(idx)

		switch val := item.(type) {
		case goja.String:
		case *goja.Object:
			if name := val.ClassName(); name != classNumber && name != classString {
				continue
			}
		default:
			if item == nil || !isNumberValue(item) {
				continue
			}
		}

		name := item.String()
		if _, dup := seen[name]; dup {
			continue
		}

		seen[name] = struct{}{}
		keys = append(keys, item.ToString())
	}

	return keys
}

// isNumberValue reports whether a primitive is a JS number.
func isNumberValue(val goja.Value) bool {
	typ := val.ExportType()
	if typ == nil {
		return false
	}

	kind := typ.Kind()

	return kind == reflect.Int64 || kind == reflect.Float64
}

// jsonSpace unwraps a Number or String object space argument, as
// JSON.stringify does, so its valueOf runs once and the indent charged is
// the indent goja writes.
func jsonSpace(space goja.Value) goja.Value {
	obj, ok := space.(*goja.Object)
	if !ok {
		return space
	}

	switch obj.ClassName() {
	case classNumber:
		return obj.ToNumber()
	case classString:
		return obj.ToString()
	default:
		return space
	}
}

// jsonGapLen is the indent length JSON.stringify derives from space.
func jsonGapLen(space goja.Value) int {
	if space == nil || goja.IsUndefined(space) {
		return 0
	}

	if _, isStr := space.(goja.String); isStr {
		return min(len(space.String()), jsonGapMax)
	}

	if !isNumberValue(space) {
		return 0
	}

	num := space.ToFloat()
	if math.IsNaN(num) || num < 1 {
		return 0
	}

	return int(math.Min(num, jsonGapMax))
}

// jsonStringify names JSON.stringify in its refusals.
const jsonStringify = "JSON.stringify"

// jsonLevel is what the charge knows of a container goja is writing.
type jsonLevel struct {
	depth int
	array bool
}

// jsonCharge is the state of one JSON.stringify call.
type jsonCharge struct {
	r       *jsRuntime
	user    goja.Callable
	keys    []goja.Value
	proxies map[*goja.Object]*goja.Object
	levels  map[*goja.Object]jsonLevel
	gap     int
	calls   int64
	started bool
}

// replace is the replacer goja calls for every value it writes, the root
// included, with the holder as this.
func (c *jsonCharge) replace(call goja.FunctionCall) goja.Value {
	key, value := call.Argument(0), call.Argument(1)

	c.calls++
	c.r.checkDeadline(c.calls)

	if c.user != nil {
		value = callOriginal(c.user, call.This, key, value)
	}

	value = c.r.unwrapJSONPrimitive(value)

	cost := float64(0)
	depth := 0

	if c.started {
		holder, _ := call.This.(*goja.Object)
		level := c.levels[holder]
		depth = level.depth

		// The separator (or the opening bracket's newline) and the indent.
		cost++
		if c.gap > 0 {
			cost += float64(1 + c.gap*depth)
		}

		if !level.array {
			cost += jsonQuotedLen(key.ToString()) + 1
			if c.gap > 0 {
				cost++
			}
		}
	}

	c.started = true

	value, valueCost := c.valueCost(value, depth)
	c.r.spendAlloc(jsonStringify, cost+valueCost)

	return value
}

// valueCost is what goja writes for value (a container's brackets and
// closing indent only: its members are charged as goja reaches them). A
// container is registered for its members, behind a Proxy for a property
// list.
func (c *jsonCharge) valueCost(value goja.Value, depth int) (goja.Value, float64) {
	switch val := value.(type) {
	case nil:
		return value, float64(len("null"))
	case goja.String:
		c.r.spendFlatten(jsonStringify, val)

		return value, jsonQuotedLen(val)
	case *goja.Object:
		if _, isFn := goja.AssertFunction(val); isFn {
			return value, float64(len("null"))
		}

		if val.ClassName() == classRawJSON {
			return value, float64(jsStringLen(val.Get("rawJSON")))
		}

		isArray := c.r.isArrayObject(val)
		if c.keys != nil && !isArray {
			val = c.listed(val)
			value = val
		}

		c.levels[val] = jsonLevel{depth: depth + 1, array: isArray}

		cost := float64(len("{}"))
		if c.gap > 0 {
			cost += float64(1 + c.gap*depth)
		}

		return value, cost
	default:
		// Numbers, booleans, null, undefined: their text, at least "null".
		return value, math.Max(float64(len(value.String())), float64(len("false")))
	}
}

// unwrapJSONPrimitive turns a Number, String or Boolean object into its
// primitive, which is what goja serializes it as.
func (r *jsRuntime) unwrapJSONPrimitive(value goja.Value) goja.Value {
	obj, ok := value.(*goja.Object)
	if !ok {
		return value
	}

	switch obj.ClassName() {
	case classNumber:
		return obj.ToNumber()
	case classString:
		return obj.ToString()
	case classBoolean:
		if b, isBool := obj.Export().(bool); isBool {
			return r.vm.ToValue(b)
		}
	}

	return value
}

// listed is obj seen through the property list: a Proxy whose own keys are
// the list, in its order, and whose values are read from obj (a getter runs
// when goja reaches it, as it would). One Proxy per object, so a cycle is
// still the original's TypeError.
func (c *jsonCharge) listed(obj *goja.Object) *goja.Object {
	if proxy, ok := c.proxies[obj]; ok {
		return proxy
	}

	runtime := c.r.vm
	proxy := runtime.NewProxy(runtime.NewObject(), &goja.ProxyTrapConfig{
		OwnKeys: func(*goja.Object) *goja.Object {
			return runtime.NewArray(toAnySlice(c.keys)...)
		},
		GetOwnPropertyDescriptor: func(*goja.Object, string) goja.PropertyDescriptor {
			return goja.PropertyDescriptor{
				Value:        goja.Undefined(),
				Writable:     goja.FLAG_TRUE,
				Enumerable:   goja.FLAG_TRUE,
				Configurable: goja.FLAG_TRUE,
			}
		},
		Get: func(_ *goja.Object, prop string, _ goja.Value) goja.Value {
			if val := obj.Get(prop); val != nil {
				return val
			}

			return goja.Undefined()
		},
	})

	proxyObj := runtime.ToValue(proxy).ToObject(runtime)
	c.proxies[obj] = proxyObj

	return proxyObj
}

func toAnySlice(vals []goja.Value) []any {
	out := make([]any, len(vals))
	for idx, val := range vals {
		out[idx] = val
	}

	return out
}

// jsonQuotes is the two quotes around a JSON string.
const jsonQuotes = 2

// jsonQuotedLen is the UTF-8 length JSON.stringify writes for str, quotes
// and escapes included.
func jsonQuotedLen(str goja.Value) float64 {
	strVal, ok := str.(goja.String)
	if !ok {
		return float64(len(str.String()) + jsonQuotes)
	}

	// An ASCII string is its own Go string: counted byte by byte, without
	// going through CharAt.
	if reflect.TypeOf(strVal).String() == asciiStringTypeName {
		text := strVal.String()
		count := float64(jsonQuotes)

		for idx := range len(text) {
			count += jsonEscapedLen(uint16(text[idx]))
		}

		return count
	}

	count := float64(jsonQuotes)
	length := strVal.Length()

	for idx := 0; idx < length; idx++ {
		unit := strVal.CharAt(idx)
		if !utf16.IsSurrogate(rune(unit)) {
			count += jsonEscapedLen(unit)

			continue
		}

		// A surrogate pair is one 4-byte character; a lone one is \uXXXX.
		if unit < 0xDC00 && idx+1 < length {
			if next := strVal.CharAt(idx + 1); next >= 0xDC00 && next <= 0xDFFF {
				count += 4
				idx++

				continue
			}
		}

		count += 6
	}

	return count
}

// jsonEscapedLen is the UTF-8 length JSON.stringify writes for one UTF-16
// unit that is not a surrogate.
func jsonEscapedLen(unit uint16) float64 {
	switch {
	case unit == '"' || unit == '\\' || unit == '\b' || unit == '\t' || unit == '\n' || unit == '\f' || unit == '\r':
		return 2
	case unit < 0x20:
		return 6
	case unit < 0x80:
		return 1
	case unit < 0x800:
		return 2
	default:
		return 3
	}
}
