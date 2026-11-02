package checkjs

import (
	"errors"
	"fmt"

	"github.com/dop251/goja"
)

const (
	// ropeScanMaxNodes bounds the values ropeBytes visits in one argument or
	// result, so the scan itself cannot be made to run forever.
	ropeScanMaxNodes = 1 << 20
	// ropeScanMaxDepth bounds how deep ropeBytes descends into nested objects.
	ropeScanMaxDepth = 64
)

// bindingGlobals are the globals whose methods hand script values to Go.
//
//nolint:gochecknoglobals // Fixed list of binding names.
var bindingGlobals = []string{
	"console", "sleep", "solidping", "http", "base64",
	"browser", "rdp", "vnc", "tcp", "udp", "websocket",
}

// guardBindings puts the flatten guard in front of every binding (spec
// 2026-11-01-01): a binding reads its string arguments in Go, which flattens
// a rope built with `+` in one native call. A rope over what is left of the
// budget is refused before the binding runs. Handles a binding returns
// (pages, sessions, sockets, …) get the same guard.
func (r *jsRuntime) guardBindings() {
	global := r.vm.GlobalObject()

	for _, name := range bindingGlobals {
		val := global.Get(name)
		if val == nil {
			continue
		}

		if fn, ok := goja.AssertFunction(val); ok {
			_ = r.vm.Set(name, r.guardBinding(name, fn))

			continue
		}

		if obj, ok := val.(*goja.Object); ok {
			r.guardObject(name, obj)
		}
	}
}

// guardObject wraps every function property of obj, once.
func (r *jsRuntime) guardObject(prefix string, obj *goja.Object) {
	if _, done := r.memory.guarded[obj]; done {
		return
	}

	r.memory.guarded[obj] = struct{}{}

	for _, key := range obj.Keys() {
		fn, ok := goja.AssertFunction(obj.Get(key))
		if !ok {
			continue
		}

		_ = obj.Set(key, r.guardBinding(prefix+"."+key, fn))
	}
}

// guardBinding charges the flattening of every rope its arguments carry
// (nested in objects and arrays too), runs the binding, and guards the
// methods of the handle it returns.
func (r *jsRuntime) guardBinding(name string, orig goja.Callable) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		scan := r.newRopeScan()
		for _, arg := range call.Arguments {
			scan.walk(arg, 0)
		}

		if scan.exhausted {
			r.throwRangeError(fmt.Sprintf(
				"%s: %s: an argument nested more than %d levels deep, or holding more than %d values, cannot be checked",
				errMemoryBudgetPrefix, name, ropeScanMaxDepth, ropeScanMaxNodes))
		}

		r.spendAlloc(name+" (a string argument built with +)", scan.bytes)

		result := callOriginal(orig, call.This, call.Arguments...)

		if obj, ok := result.(*goja.Object); ok {
			if _, isFn := goja.AssertFunction(obj); !isFn {
				r.guardObject(name+"()", obj)
			}
		}

		return result
	}
}

// guardResult refuses a script result whose export would flatten ropes past
// the budget. It runs after the script, outside the VM, so it returns an
// error instead of throwing.
func (r *jsRuntime) guardResult(val goja.Value) error {
	scan := r.newRopeScan()
	scan.walk(val, 0)

	return r.scanRefusal("the script result", scan)
}

// guardThrown refuses an uncaught exception whose message would flatten
// ropes past the budget: err.Error() reads the thrown value as a string.
// Any other error passes as is.
func (r *jsRuntime) guardThrown(err error) error {
	var exc *goja.Exception
	if !errors.As(err, &exc) || exc.Value() == nil {
		return err
	}

	scan := r.newRopeScan()
	scan.walk(exc.Value(), 0)

	if refusal := r.scanRefusal("the thrown value", scan); refusal != nil {
		return refusal
	}

	return err
}

// scanRefusal is the error for a value whose ropes do not fit what is left
// of the budget, or that is too deep or too large to scan: what the scan did
// not see is not assumed to be free.
func (r *jsRuntime) scanRefusal(what string, scan *ropeScan) error {
	if scan.exhausted {
		//nolint:err113 // Built per call: carries the limits.
		return fmt.Errorf(
			"RangeError: %s: %s is nested more than %d levels deep, or holds more than %d values, and cannot be checked",
			errMemoryBudgetPrefix, what, ropeScanMaxDepth, ropeScanMaxNodes)
	}

	if scan.bytes > 0 && !r.fits(scan.bytes) {
		//nolint:err113 // Built per call: carries the sizes.
		return fmt.Errorf(
			"RangeError: %s: %s would allocate %.0f bytes; a JS check may allocate at most %d MiB in total",
			errMemoryBudgetPrefix, what, scan.bytes, maxScriptAllocBytes>>20)
	}

	return nil
}

// ropeScan sums the unflattened ropes in a value graph.
type ropeScan struct {
	seen  map[*goja.Object]struct{}
	nodes int
	bytes float64
	// limit is what is left of the budget: the scan stops once past it.
	limit float64
	// exhausted is set when the scan hit its depth or node cap: the part it
	// did not visit may hold a rope, so the value is refused.
	exhausted bool
}

// newRopeScan starts a scan bounded by what is left of the budget.
func (r *jsRuntime) newRopeScan() *ropeScan {
	return &ropeScan{
		seen:  map[*goja.Object]struct{}{},
		limit: float64(maxScriptAllocBytes - r.memory.used),
	}
}

func (s *ropeScan) walk(val goja.Value, depth int) {
	if s.done() || val == nil {
		return
	}

	s.nodes++
	if s.nodes > ropeScanMaxNodes || depth > ropeScanMaxDepth {
		s.exhausted = true

		return
	}

	if n := unflattenedRopeLen(val); n > 0 {
		s.bytes += n

		return
	}

	if obj, ok := val.(*goja.Object); ok {
		s.walkObject(obj, depth)
	}
}

// done reports whether the scan already has its answer: over the limit, or
// past what it can check.
func (s *ropeScan) done() bool {
	return s.exhausted || s.bytes > s.limit
}

func (s *ropeScan) walkObject(obj *goja.Object, depth int) {
	if _, done := s.seen[obj]; done {
		return
	}

	s.seen[obj] = struct{}{}

	if _, isFn := goja.AssertFunction(obj); isFn {
		return
	}

	// An Error's name and message are not enumerable, and listing its keys
	// formats its stack, which copies them: they are counted twice, and the
	// keys are only listed when that fits.
	if obj.ClassName() == classError {
		before := s.bytes
		s.walk(obj.Get("name"), depth+1)
		s.walk(obj.Get("message"), depth+1)
		s.bytes += s.bytes - before

		if s.done() {
			return
		}
	}

	// Typed arrays, buffers and wrapped Go values hold no JS strings.
	if _, typed := typedArrayLength(obj); typed || obj.ExportType() == arrayBufferType {
		return
	}

	if isRealArray(obj) {
		length := int64(toLength(obj.Get("length")))
		for idx := int64(0); idx < length && !s.done(); idx++ {
			s.walk(obj.GetIdx(idx), depth+1)
		}

		return
	}

	for _, key := range obj.Keys() {
		if s.done() {
			return
		}

		s.walk(obj.Get(key), depth+1)
	}
}
