package checkjs

import (
	"strconv"
	"strings"

	"github.com/dop251/goja"
)

// methodExec is RegExp.prototype.exec's name.
const methodExec = "exec"

// The per-match limit (maxRegexpMatchTime) bounds one match, not one call:
// goja runs the whole match loop of a global replace, a global match and
// every split natively, inside one call that vm.Interrupt never reaches, and
// each match gets a fresh 2 s. A pattern that backtracks for 1.5 s before it
// matches, over an input that repeats that segment 1,000 times (25 KB), kept
// a core busy for hours (spec 2026-11-02-02).
//
// installRegexpDeadline moves those loops onto RegExp.prototype.exec, one
// call per match, and makes exec and test check the run's deadline first:
// past it, the next match throws instead of starting. A run then overshoots
// its timeout by one match at most.
//
//   - replace and match with a global regexp run goja's own spec loop (one
//     exec per match) on a Proxy of the regexp, which goja cannot take its
//     native shortcut on.
//   - split always runs its native loop, and goja's spec loop calls exec
//     once per input position, which is quadratic. Its loop is ported here,
//     driven by exec on a global copy of the pattern.
//   - matchAll and search already call exec once per match.
func (r *jsRuntime) installRegexpDeadline(regexpCtor, regexpProto *goja.Object) {
	for _, name := range []string{methodExec, "test"} {
		r.wrapMethod(regexpProto, name, 1, r.deadlineGuard)
	}

	if exec, ok := goja.AssertFunction(regexpProto.Get(methodExec)); ok {
		r.memory.regexpExec = exec
	}

	r.wrapSymbol(regexpProto, goja.SymReplace, r.execDrivenGlobal)
	r.wrapSymbol(regexpProto, goja.SymMatch, r.execDrivenGlobal)
	r.wrapSymbol(regexpProto, goja.SymSplit, func(orig goja.Callable) func(goja.FunctionCall) goja.Value {
		return r.boundedRegexpSplit(regexpCtor, regexpProto, orig)
	})
}

// wrapSymbol replaces obj[sym] with a guard built around the original,
// keeping its name and length.
func (r *jsRuntime) wrapSymbol(
	obj *goja.Object, sym *goja.Symbol,
	wrap func(orig goja.Callable) func(goja.FunctionCall) goja.Value,
) {
	val := obj.GetSymbol(sym)

	orig, ok := goja.AssertFunction(val)
	if !ok {
		return
	}

	origObj := val.ToObject(r.vm)
	fn := r.methodObject(origObj.Get("name").String(), int(toLength(origObj.Get("length"))), wrap(orig))
	_ = obj.DefineDataPropertySymbol(sym, fn, goja.FLAG_TRUE, goja.FLAG_TRUE, goja.FLAG_FALSE)
}

// checkRegexpDeadline throws once the run's deadline has passed (or the run
// was canceled): resultFor reports it as the check's timeout.
func (r *jsRuntime) checkRegexpDeadline() {
	if err := r.execCtx.Err(); err != nil {
		panic(r.vm.NewGoError(err))
	}
}

// deadlineGuard checks the run's deadline before every exec and test.
func (r *jsRuntime) deadlineGuard(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		r.checkRegexpDeadline()

		return callOriginal(orig, call.This, call.Arguments...)
	}
}

// execDrivenGlobal runs Symbol.replace or Symbol.match with a global regexp
// on a Proxy of it: goja's native shortcut needs the regexp itself, so it
// takes the spec loop, one exec per match. A regexp with an exec of its own
// already takes that loop, and a non-global one matches once.
func (r *jsRuntime) execDrivenGlobal(orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		regex, ok := call.This.(*goja.Object)
		if !ok || regex.ClassName() != classRegExp || hasOwn(regex, methodExec) {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		flags := regex.Get("flags")
		if flags == nil || !strings.Contains(flags.String(), "g") {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		return callOriginal(orig, r.execProxy(regex), call.Arguments...)
	}
}

// execProxy is regex seen through a Proxy that reads and writes regex itself, so
// its accessors (flags, global, …) and exec still see a RegExp receiver.
func (r *jsRuntime) execProxy(regex *goja.Object) goja.Value {
	proxy := r.vm.NewProxy(regex, &goja.ProxyTrapConfig{
		Get: func(target *goja.Object, property string, _ goja.Value) goja.Value {
			val := target.Get(property)
			if property != methodExec {
				return val
			}

			exec, ok := goja.AssertFunction(val)
			if !ok {
				return val
			}

			return r.vm.ToValue(func(c goja.FunctionCall) goja.Value {
				return callOriginal(exec, target, c.Arguments...)
			})
		},
		Set: func(target *goja.Object, property string, value, _ goja.Value) bool {
			if err := target.Set(property, value); err != nil {
				panic(err)
			}

			return true
		},
	})

	return r.vm.ToValue(proxy)
}

// hasOwn reports whether obj has an own string-keyed property name.
func hasOwn(obj *goja.Object, name string) bool {
	for _, key := range obj.GetOwnPropertyNames() {
		if key == name {
			return true
		}
	}

	return false
}

// isStdRegexp reports whether goja takes its native shortcut on obj: a
// RegExp whose prototype is RegExp.prototype and whose only own property is
// lastIndex. It errs on "no", whose loop gives the same result.
func isStdRegexp(obj, regexpProto *goja.Object) bool {
	if obj.ClassName() != classRegExp || obj.Prototype() == nil || !obj.Prototype().SameAs(regexpProto) {
		return false
	}

	keys := obj.GetOwnPropertyNames()

	return len(keys) == 1 && keys[0] == "lastIndex" && len(obj.Symbols()) == 0
}

// boundedRegexpSplit is RegExp.prototype[Symbol.split] with its loop in Go,
// one exec per match. It takes the same two paths as goja's: the splitter
// (the regexp's species constructor called with its flags plus "y") is
// split with the native algorithm when goja would shortcut it, with the
// spec's per-position loop otherwise.
func (r *jsRuntime) boundedRegexpSplit(
	regexpCtor, regexpProto *goja.Object, orig goja.Callable,
) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		regex, ok := call.This.(*goja.Object)
		if !ok || r.memory.regexpExec == nil {
			return callOriginal(orig, call.This, call.Arguments...)
		}

		input, _ := call.Argument(0).ToString().(goja.String)
		limit := call.Argument(1)

		ctor := r.speciesConstructor(regex, regexpCtor)

		if ctor.SameAs(regexpCtor) {
			// goja splits on regex itself when it is standard, else on a fresh
			// RegExp of the same pattern: either way, the native algorithm.
			return r.splitByMatches(r.globalCopy(regexpCtor, regex, ""), input, limit)
		}

		flags := r.flagsOf(regex)
		unicode := strings.Contains(flags, "u")

		if !strings.Contains(flags, "y") {
			flags += "y"
		}

		splitter := r.construct(ctor, regex, r.vm.ToValue(flags))

		if isStdRegexp(splitter, regexpProto) {
			return r.splitByMatches(r.globalCopy(regexpCtor, splitter, flags), input, limit)
		}

		return r.splitByPosition(splitter, input, limit, unicode)
	}
}

// flagsOf reads regex.flags as a string.
func (r *jsRuntime) flagsOf(regex *goja.Object) string {
	val := regex.Get("flags")
	if val == nil {
		return "undefined"
	}

	return val.String()
}

// speciesConstructor is ECMAScript's SpeciesConstructor.
func (r *jsRuntime) speciesConstructor(obj, defaultCtor *goja.Object) *goja.Object {
	ctorVal := obj.Get("constructor")
	if ctorVal == nil || goja.IsUndefined(ctorVal) {
		return defaultCtor
	}

	ctorObj, ok := ctorVal.(*goja.Object)
	if !ok {
		panic(r.vm.NewTypeError("object.constructor is not an object"))
	}

	species := ctorObj.GetSymbol(goja.SymSpecies)
	if species == nil || goja.IsUndefined(species) || goja.IsNull(species) {
		return defaultCtor
	}

	speciesObj, ok := species.(*goja.Object)
	if _, isCtor := goja.AssertConstructor(species); !ok || !isCtor {
		panic(r.vm.NewTypeError("Species is not a constructor"))
	}

	return speciesObj
}

// construct runs `new ctor(args…)`.
func (r *jsRuntime) construct(ctor *goja.Object, args ...goja.Value) *goja.Object {
	newFn, ok := goja.AssertConstructor(ctor)
	if !ok {
		panic(r.vm.NewTypeError("not a constructor"))
	}

	obj, err := newFn(ctor, args...)
	if err != nil {
		panic(err)
	}

	return obj
}

// globalCopy is a fresh global, non-sticky RegExp of regex's pattern: the
// native split scans every match from the start, whatever regex's flags and
// lastIndex. flags, when set, replaces regex.flags.
func (r *jsRuntime) globalCopy(regexpCtor, regex *goja.Object, flags string) *goja.Object {
	if flags == "" {
		flags = r.flagsOf(regex)
	}

	flags = strings.ReplaceAll(flags, "y", "")
	if !strings.Contains(flags, "g") {
		flags += "g"
	}

	return r.construct(regexpCtor, regex, r.vm.ToValue(flags))
}

// regexpMatch is one exec result: where the match is and its captures.
type regexpMatch struct {
	start, end int
	captures   []goja.Value
}

// nextMatch runs exec on a global regexp and reads its result, or returns
// false when there is no further match. An empty match moves lastIndex on
// by one character, as goja's global loop does.
func (r *jsRuntime) nextMatch(regex *goja.Object, input goja.String, unicode bool) (regexpMatch, bool) {
	res := callOriginal(r.memory.regexpExec, regex, input)
	if goja.IsNull(res) || goja.IsUndefined(res) {
		return regexpMatch{}, false
	}

	obj := res.ToObject(r.vm)
	matched, _ := obj.Get("0").ToString().(goja.String)
	start := int(obj.Get("index").ToInteger())
	end := start + matched.Length()

	if matched.Length() == 0 {
		last := int(regex.Get("lastIndex").ToInteger())
		_ = regex.Set("lastIndex", advanceIndex(input, last, unicode))
	}

	count := int(toLength(obj.Get("length")))
	captures := make([]goja.Value, 0, max(count-1, 0))

	for i := 1; i < count; i++ {
		capture := obj.Get(strconv.Itoa(i))
		if capture == nil {
			capture = goja.Undefined()
		}

		captures = append(captures, capture)
	}

	return regexpMatch{start: start, end: end, captures: captures}, true
}

// advanceIndex is ECMAScript's AdvanceStringIndex.
func advanceIndex(input goja.String, pos int, unicode bool) int {
	next := pos + 1
	if !unicode || next >= input.Length() {
		return next
	}

	if isHighSurrogate(input.CharAt(pos)) && isLowSurrogate(input.CharAt(next)) {
		return next + 1
	}

	return next
}

func isHighSurrogate(unit uint16) bool { return unit >= 0xD800 && unit <= 0xDBFF }

func isLowSurrogate(unit uint16) bool { return unit >= 0xDC00 && unit <= 0xDFFF }

// splitByMatches is goja's native split (regexpproto_stdSplitter) over the
// matches of regex, a global copy of the pattern, read one exec at a time.
//
//nolint:cyclop // a line-by-line port: kept whole to stay comparable.
func (r *jsRuntime) splitByMatches(regex *goja.Object, input goja.String, limitVal goja.Value) goja.Value {
	limit := -1
	if !goja.IsUndefined(limitVal) {
		limit = int(toUint32(limitVal))
	}

	if limit == 0 {
		return r.vm.NewArray()
	}

	unicode := strings.Contains(r.flagsOf(regex), "u")
	targetLength := input.Length()

	_ = regex.Set("lastIndex", 0)

	if targetLength == 0 {
		if _, found := r.nextMatch(regex, input, unicode); found {
			return r.vm.NewArray()
		}

		return r.vm.NewArray(input)
	}

	var values []goja.Value

	lastIndex := 0
	found := 0

	for {
		match, ok := r.nextMatch(regex, input, unicode)
		if !ok {
			break
		}

		if match.start == match.end &&
			(match.start == 0 || match.start == targetLength || match.start == lastIndex) {
			continue
		}

		if lastIndex != match.start {
			values = append(values, input.Substring(lastIndex, match.start))
		} else {
			values = append(values, r.emptyString())
		}

		found++
		lastIndex = match.end

		if found == limit {
			return r.vm.NewArray(toAnySlice(values)...)
		}

		for _, capture := range match.captures {
			values = append(values, capture)

			found++
			if found == limit {
				return r.vm.NewArray(toAnySlice(values)...)
			}
		}
	}

	if lastIndex != targetLength {
		values = append(values, input.Substring(lastIndex, targetLength))
	} else {
		values = append(values, r.emptyString())
	}

	return r.vm.NewArray(toAnySlice(values)...)
}

// execOn is ECMAScript's RegExpExec: splitter's own exec when it has one,
// else RegExp.prototype.exec.
func (r *jsRuntime) execOn(splitter *goja.Object, input goja.String) goja.Value {
	exec, ok := goja.AssertFunction(splitter.Get(methodExec))
	if !ok {
		exec = r.memory.regexpExec
	}

	res := callOriginal(exec, splitter, input)
	if _, isObj := res.(*goja.Object); !isObj && !goja.IsNull(res) {
		panic(r.vm.NewTypeError("RegExp exec method returned something other than an Object or null"))
	}

	return res
}

// splitByPosition is the spec's split loop (goja's
// regexpproto_stdSplitterGeneric): a sticky exec at every position, for a
// splitter goja cannot shortcut (a RegExp subclass and the like).
func (r *jsRuntime) splitByPosition(
	splitter *goja.Object, input goja.String, limitVal goja.Value, unicode bool,
) goja.Value {
	lim := float64(1<<53 - 1)
	if !goja.IsUndefined(limitVal) {
		lim = toLength(limitVal)
	}

	if lim == 0 {
		return r.vm.NewArray()
	}

	size := input.Length()

	if size == 0 {
		if goja.IsNull(r.execOn(splitter, input)) {
			return r.vm.NewArray(input)
		}

		return r.vm.NewArray()
	}

	var values []goja.Value

	full := func() bool { return float64(len(values)) == lim }

	pos, next := 0, 0
	for next < size {
		r.checkRegexpDeadline()
		_ = splitter.Set("lastIndex", next)

		res := r.execOn(splitter, input)
		if goja.IsNull(res) {
			next = advanceIndex(input, next, unicode)

			continue
		}

		end := min(int(toLength(splitter.Get("lastIndex"))), size)
		if end == pos {
			next = advanceIndex(input, next, unicode)

			continue
		}

		values = append(values, input.Substring(pos, next))
		if full() {
			return r.vm.NewArray(toAnySlice(values)...)
		}

		pos = end

		obj := res.ToObject(r.vm)
		for i := 1; i < int(toLength(obj.Get("length"))); i++ {
			capture := obj.Get(strconv.Itoa(i))
			if capture == nil {
				capture = goja.Undefined()
			}

			values = append(values, capture)
			if full() {
				return r.vm.NewArray(toAnySlice(values)...)
			}
		}

		next = pos
	}

	values = append(values, input.Substring(pos, size))

	return r.vm.NewArray(toAnySlice(values)...)
}
