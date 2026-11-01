package checkjs

import (
	"math"
	"reflect"
	"strings"

	"github.com/dop251/goja"
)

// ropeTypeName is the Go type goja builds `a + b` as once the result is over
// 32 characters: a rope that holds both halves and copies them into one flat
// string the first time something needs the characters. TestRopeDetection
// fails if a goja upgrade renames it.
const ropeTypeName = "*goja.concatString"

// ropeFreeLen is the rope length flattened without a charge: copying 64 KiB
// costs what any uncharged string method (toUpperCase, slice, …) does, and a
// script that caught a budget refusal can still log `"failed: " + e.message`.
const ropeFreeLen = 64 * 1024

// unflattenedRopeLen is the length of v if it is a rope goja has not
// flattened yet and is longer than ropeFreeLen, else 0. Flattening allocates the whole string in one native
// call (spec 2026-11-01-01): doubling a string 28 times is 28 instructions
// and allocates nothing, the first read of a character allocates 256 MB.
func unflattenedRopeLen(v goja.Value) float64 {
	str, ok := v.(goja.String)
	if !ok || reflect.TypeOf(str).String() != ropeTypeName {
		return 0
	}

	// A flattened rope answers ToString with its flat string.
	if reflect.TypeOf(str.ToString()).String() != ropeTypeName {
		return 0
	}

	if str.Length() <= ropeFreeLen {
		return 0
	}

	return float64(str.Length())
}

// spendFlatten charges the flattening of v when it is an unflattened rope,
// then flattens it, so the same rope is charged once whoever reads it next.
func (r *jsRuntime) spendFlatten(what string, val goja.Value) {
	n := unflattenedRopeLen(val)
	if n <= 0 {
		return
	}

	r.spendAlloc(what+" (flattening a string built with +)", n)

	_ = val.(goja.String).CharAt(0) //nolint:forcetypeassert // unflattenedRopeLen checked it.
}

// installStringCaps caps String.prototype.split, replace and replaceAll, then
// puts a flatten guard in front of every String.prototype and RegExp method:
// a rope over the budget is refused before goja flattens it. The guard does
// not reach the flattens that happen outside a call (`s[0]`, `s == t`,
// `Number(s)`, a template literal): only running the check in a process of
// its own with a hard memory limit closes those.
func (r *jsRuntime) installStringCaps(stringProto *goja.Object) {
	r.wrapMethod(stringProto, "split", 2, r.capSplit)
	r.wrapMethod(stringProto, "replace", 2, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capReplace("String.prototype.replace", false, orig)
	})
	r.wrapMethod(stringProto, "replaceAll", 2, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.capReplace("String.prototype.replaceAll", true, orig)
	})

	for _, name := range stringProto.GetOwnPropertyNames() {
		if name != "constructor" {
			r.guardMethod(stringProto, "String.prototype."+name, name)
		}
	}

	regexpCtor, ok := r.vm.Get("RegExp").(*goja.Object)
	if !ok {
		return
	}

	regexpProto, ok := regexpCtor.Get("prototype").(*goja.Object)
	if !ok {
		return
	}

	// Named, not walked: RegExp.prototype also holds accessors (flags,
	// source, …) that must stay accessors.
	for _, name := range []string{"exec", "test", "compile"} {
		r.guardMethod(regexpProto, "RegExp.prototype."+name, name)
	}

	for _, sym := range []*goja.Symbol{
		goja.SymSplit, goja.SymReplace, goja.SymMatch, goja.SymMatchAll, goja.SymSearch,
	} {
		val := regexpProto.GetSymbol(sym)

		orig, ok := goja.AssertFunction(val)
		if !ok {
			continue
		}

		origObj := val.ToObject(r.vm)
		name := origObj.Get("name").String()
		fn := r.methodObject(name, int(toLength(origObj.Get("length"))), r.flattenGuard("RegExp.prototype"+name, orig))
		_ = regexpProto.DefineDataPropertySymbol(sym, fn, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	}

	r.memory.regexpReplace = regexpProto.GetSymbol(goja.SymReplace)
}

// guardMethod puts the flatten guard in front of obj[name] when it is a
// function, keeping its name and length.
func (r *jsRuntime) guardMethod(obj *goja.Object, what, name string) {
	val := obj.Get(name)

	orig, ok := goja.AssertFunction(val)
	if !ok {
		return
	}

	length := int(toLength(val.ToObject(r.vm).Get("length")))
	r.setMethod(obj, name, length, r.flattenGuard(what, orig))
}

// methodObject builds a builtin-shaped function object.
func (r *jsRuntime) methodObject(name string, length int, fn func(goja.FunctionCall) goja.Value) *goja.Object {
	fnObj := r.vm.ToValue(fn).ToObject(r.vm)
	_ = fnObj.DefineDataProperty("name", r.vm.ToValue(name), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)
	_ = fnObj.DefineDataProperty("length", r.vm.ToValue(length), goja.FLAG_FALSE, goja.FLAG_TRUE, goja.FLAG_FALSE)

	return fnObj
}

// flattenGuard charges the flattening of a rope receiver or argument, then
// runs the original.
func (r *jsRuntime) flattenGuard(what string, orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		r.spendFlatten(what, call.This)

		for _, arg := range call.Arguments {
			r.spendFlatten(what, arg)
		}

		return callOriginal(orig, call.This, call.Arguments...)
	}
}

// toUint32 is ECMAScript's ToUint32.
func toUint32(v goja.Value) float64 {
	f := v.ToFloat()
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}

	return float64(uint32(int64(math.Mod(math.Trunc(f), 1<<32))))
}

// capSplit charges String.prototype.split for its parts, 16 bytes each:
// `s.split("")` turns every character into an array slot in one call. A
// string separator is counted exactly; a RegExp one is bounded by one part
// per character (times its capture groups) before the call and charged
// exactly after it.
func (r *jsRuntime) capSplit(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	const what = "String.prototype.split"

	return func(call goja.FunctionCall) goja.Value {
		if goja.IsUndefined(call.This) || goja.IsNull(call.This) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		sep := call.Argument(0)

		if obj, ok := sep.(*goja.Object); ok {
			if splitter := obj.GetSymbol(goja.SymSplit); splitter != nil &&
				!goja.IsUndefined(splitter) && !goja.IsNull(splitter) {
				return r.regexpSplit(orig, call, obj)
			}
		}

		str := call.This.ToString()
		r.spendFlatten(what, str)

		limit := math.Inf(1)
		limitArg := call.Argument(1)

		if !goja.IsUndefined(limitArg) {
			limit = toUint32(limitArg)
			limitArg = r.vm.ToValue(limit)
		}

		if !goja.IsUndefined(sep) {
			sep = sep.ToString()
			r.spendFlatten(what, sep)
		}

		parts := math.Min(splitParts(str, sep), limit)
		r.spendAlloc(what, parts*jsValueSlotBytes)

		result := callOriginal(orig, str, sep, limitArg)

		// The count runs on UTF-8: a lone surrogate may count differently.
		if extra := r.lengthOf(result) - parts; extra > 0 {
			r.spendAlloc(what, extra*jsValueSlotBytes)
		}

		return result
	}
}

// regexpSplit runs split with a separator that has its own Symbol.split.
func (r *jsRuntime) regexpSplit(orig goja.Callable, call goja.FunctionCall, sep *goja.Object) goja.Value {
	const what = "String.prototype.split"

	str := call.This.ToString()
	r.spendFlatten(what, str)

	limit := math.Inf(1)
	limitArg := call.Argument(1)

	if !goja.IsUndefined(limitArg) {
		limit = toUint32(limitArg)
		limitArg = r.vm.ToValue(limit)
	}

	groups := 0.0
	if source := sep.Get("source"); source != nil && !goja.IsUndefined(source) {
		groups = float64(strings.Count(source.String(), "("))
	}

	bound := math.Min((float64(jsStringLen(str))+1)*(groups+1), limit)
	r.checkAlloc(what, bound*jsValueSlotBytes)

	result := callOriginal(orig, str, sep, limitArg)
	r.spendAlloc(what, r.lengthOf(result)*jsValueSlotBytes)

	return result
}

// splitParts counts the parts a string separator splits str into.
func splitParts(str, sep goja.Value) float64 {
	if goja.IsUndefined(sep) {
		return 1
	}

	strLen := jsStringLen(str)

	if jsStringLen(sep) == 0 {
		return float64(strLen)
	}

	return float64(strings.Count(str.String(), sep.String()) + 1)
}

// capReplace charges replace/replaceAll for their output: the input once, at
// the first match, plus every replacement as it is produced. The replacement
// goes to goja as a Go function, so each expansion ($&, the $` prefix, the
// $' suffix, $1, or what a replacer function returns) is measured and charged before
// goja appends it: `s.replaceAll("a", "$&$&$&…")` cannot multiply the input
// in one call any more. A pattern with a Symbol.replace of its own (not a
// RegExp) is left alone: it is script code, and runs as such.
func (r *jsRuntime) capReplace(what string, all bool, orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		if goja.IsUndefined(call.This) || goja.IsNull(call.This) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		pattern := call.Argument(0)
		repl := call.Argument(1)

		if obj, ok := pattern.(*goja.Object); ok {
			replacer, isObj := obj.GetSymbol(goja.SymReplace).(*goja.Object)
			if isObj && !replacer.SameAs(r.memory.regexpReplace) {
				return callOriginal(orig, call.This, call.Arguments...)
			}
		} else {
			pattern = pattern.ToString()
			r.spendFlatten(what, pattern)
		}

		str := call.This.ToString()
		r.spendFlatten(what, str)

		inputLen := float64(jsStringLen(str))
		charged := false

		chargeInput := func() {
			if !charged {
				charged = true
				r.spendAlloc(what, inputLen)
			}
		}

		var replacer func(goja.FunctionCall) goja.Value

		if fn, ok := goja.AssertFunction(repl); ok {
			replacer = func(c goja.FunctionCall) goja.Value {
				chargeInput()

				out := callOriginal(fn, goja.Undefined(), c.Arguments...).ToString()
				r.spendAlloc(what, float64(jsStringLen(out)))

				return out
			}
		} else {
			replStr, _ := repl.ToString().(goja.String)
			r.spendFlatten(what, replStr)

			// A string pattern with a template that does not depend on the
			// match's position expands the same way at every match: charge
			// it all up front and let goja's native path run.
			patStr, isPat := pattern.(goja.String)
			input, isInput := str.(goja.String)

			if isPat && isInput && !positionalTemplate(replStr) {
				matches := countMatches(input, patStr, all)
				if matches > 0 {
					sub := substitution{repl: replStr, input: patStr, empty: r.emptyString(), captures: []goja.Value{patStr}}
					perMatch := float64(sub.length() - patStr.Length())
					r.spendAlloc(what, inputLen+matches*perMatch)
				}

				return callOriginal(orig, str, pattern, replStr)
			}

			replacer = func(c goja.FunctionCall) goja.Value {
				chargeInput()

				return r.substitute(what, replStr, c.Arguments)
			}
		}

		return callOriginal(orig, str, pattern, r.vm.ToValue(replacer))
	}
}

// substitute expands a replacement template for one match, as goja's own
// writeSubstitution does, after charging its length. args are what goja
// hands a replacer function: the match, the captures, the position, the
// input and, when the pattern has named groups, the groups object.
func (r *jsRuntime) substitute(what string, repl goja.String, args []goja.Value) goja.Value {
	sub := substitution{repl: repl, empty: r.emptyString()}

	count := len(args)
	if count > 0 {
		if groups, ok := args[count-1].(*goja.Object); ok {
			sub.groups = groups
			count--
		}
	}

	if count < 3 {
		return repl
	}

	sub.input, _ = args[count-1].ToString().(goja.String)
	sub.position = int(args[count-2].ToInteger())
	sub.captures = args[:count-2]

	size := sub.length()
	r.spendAlloc(what, float64(size))

	var buf goja.StringBuilder

	buf.Grow(size)
	sub.walk(buf.WriteString, func(c rune) { buf.WriteRune(c) })

	return buf.String()
}

// emptyString is the JS empty string.
func (r *jsRuntime) emptyString() goja.String {
	return r.vm.ToValue("").(goja.String) //nolint:forcetypeassert // ToValue of a Go string is a goja.String.
}

// positionalTemplate reports whether a replacement template uses $` or $',
// whose expansion depends on where the match is.
func positionalTemplate(repl goja.String) bool {
	text := repl.String()

	return strings.Contains(text, "$`") || strings.Contains(text, "$'")
}

// countMatches counts the matches of a string pattern: the first one only
// for replace, every non-overlapping one for replaceAll.
func countMatches(str, pattern goja.String, all bool) float64 {
	if pattern.Length() == 0 {
		if all {
			return float64(str.Length() + 1)
		}

		return 1
	}

	n := strings.Count(str.String(), pattern.String())
	if !all {
		n = min(n, 1)
	}

	return float64(n)
}

// substitution is one match's replacement template and what it refers to.
type substitution struct {
	repl     goja.String
	input    goja.String
	empty    goja.String
	groups   *goja.Object
	captures []goja.Value
	position int
}

// length is the expansion's length, in UTF-16 units.
func (s *substitution) length() int {
	size := 0
	s.walk(func(piece goja.String) { size += piece.Length() }, func(rune) { size++ })

	return size
}

func (s *substitution) capture(idx int) goja.String {
	if idx >= len(s.captures) {
		return s.empty
	}

	v := s.captures[idx]
	if v == nil || goja.IsUndefined(v) {
		return s.empty
	}

	str, _ := v.ToString().(goja.String)

	return str
}

// namedCapture is nil when the pattern has no named groups (the `$<` stays
// literal), empty for an unknown or unmatched name.
func (s *substitution) namedCapture(name goja.String) goja.String {
	if s.groups == nil {
		return nil
	}

	v := s.groups.Get(name.String())
	if v == nil || goja.IsUndefined(v) {
		return s.empty
	}

	str, _ := v.ToString().(goja.String)

	return str
}

// walk is goja's writeSubstitution, emitting pieces instead of writing them,
// so the same walk measures the expansion and then builds it.
//
//nolint:cyclop,gocognit,funlen // A faithful port of goja's template parser.
func (s *substitution) walk(emit func(goja.String), emitRune func(rune)) {
	inputLen := s.input.Length()
	replLen := s.repl.Length()
	matched := s.capture(0)
	tailPos := s.position + matched.Length()

	for i := 0; i < replLen; i++ {
		c := s.repl.CharAt(i)
		if c != '$' || i >= replLen-1 {
			emitRune(rune(c))

			continue
		}

		next := s.repl.CharAt(i + 1)

		switch next {
		case '$':
			emitRune('$')
		case '`':
			emit(s.input.Substring(0, min(s.position, inputLen)))
		case '\'':
			if tailPos < inputLen {
				emit(s.input.Substring(tailPos, inputLen))
			}
		case '&':
			emit(matched)
		case '<':
			var ref goja.String

			j := i + 2
			for ; j < replLen; j++ {
				if s.repl.CharAt(j) == '>' {
					ref = s.repl.Substring(i+2, j)

					break
				}
			}

			if ref != nil {
				if capture := s.namedCapture(ref); capture != nil {
					emit(capture)

					i = j

					continue
				}
			}

			emitRune('$')
			emitRune('<')
		default:
			index := 0
			j := i + 1
			lim := min(j+2, replLen)

			for ; j < lim; j++ {
				d := s.repl.CharAt(j)
				if d < '0' || d > '9' {
					break
				}

				m := index*10 + int(d-'0')
				if m >= len(s.captures) {
					break
				}

				index = m
			}

			if index > 0 {
				emit(s.capture(index))

				i = j - 1

				continue
			}

			emitRune('$')
			emitRune(rune(next))
		}

		i++
	}
}
