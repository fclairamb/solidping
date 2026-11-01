package checkjs

import (
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
		scan := ropeScan{seen: map[*goja.Object]struct{}{}}
		for _, arg := range call.Arguments {
			scan.walk(arg, 0)
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
	scan := ropeScan{seen: map[*goja.Object]struct{}{}}
	scan.walk(val, 0)

	if scan.bytes > 0 && !r.fits(scan.bytes) {
		//nolint:err113 // Built per call: carries the sizes.
		return fmt.Errorf(
			"RangeError: %s: the script result would allocate %.0f bytes; a JS check may allocate at most %d MiB in total",
			errMemoryBudgetPrefix, scan.bytes, maxScriptAllocBytes>>20)
	}

	return nil
}

// ropeScan sums the unflattened ropes in a value graph.
type ropeScan struct {
	seen  map[*goja.Object]struct{}
	nodes int
	bytes float64
}

func (s *ropeScan) walk(val goja.Value, depth int) {
	s.nodes++
	if val == nil || s.nodes > ropeScanMaxNodes || depth > ropeScanMaxDepth {
		return
	}

	if n := unflattenedRopeLen(val); n > 0 {
		s.bytes += n

		return
	}

	obj, ok := val.(*goja.Object)
	if !ok {
		return
	}

	if _, done := s.seen[obj]; done {
		return
	}

	s.seen[obj] = struct{}{}

	if _, isFn := goja.AssertFunction(obj); isFn {
		return
	}

	// Typed arrays, buffers and wrapped Go values hold no JS strings.
	if _, typed := typedArrayLength(obj); typed || obj.ExportType() == arrayBufferType {
		return
	}

	if isRealArray(obj) {
		length := int64(toLength(obj.Get("length")))
		for idx := int64(0); idx < length && s.nodes <= ropeScanMaxNodes; idx++ {
			s.walk(obj.GetIdx(idx), depth+1)
		}

		return
	}

	for _, key := range obj.Keys() {
		if s.nodes > ropeScanMaxNodes {
			return
		}

		s.walk(obj.Get(key), depth+1)
	}
}
