package testsupport

import (
	"context"
	"reflect"
	"testing"
	"time"
)

// sweepCallTimeout bounds one swept method call, so a method that blocks on
// its placeholder arguments cannot hang the suite.
const sweepCallTimeout = 10 * time.Second

// CallErrorMethods calls every exported method of target whose last result is
// an error, with placeholder arguments (the test context, "x" strings, empty
// collections, zeroed structs behind non-nil pointers). It is meant for a
// target whose backing database has been closed: each method must then reach
// its first query and surface the failure as an error. A panic on the
// placeholders (a nil interface dereferenced before the first query) is
// recovered and counted as not failed. It returns how many methods were
// called and how many returned an error.
func CallErrorMethods(t *testing.T, target any, skip ...string) (int, int) {
	t.Helper()

	skipped := make(map[string]bool, len(skip))
	for _, name := range skip {
		skipped[name] = true
	}

	value := reflect.ValueOf(target)
	errType := reflect.TypeFor[error]()

	var called, failed int

	for i := range value.NumMethod() {
		name := value.Type().Method(i).Name
		method := value.Method(i)
		sig := method.Type()

		if skipped[name] || sig.NumOut() == 0 || sig.Out(sig.NumOut()-1) != errType {
			continue
		}

		args := make([]reflect.Value, sig.NumIn())
		for j := range args {
			args[j] = placeholder(t.Context(), sig.In(j))
		}

		called++

		if callWithTimeout(method, args, sig.IsVariadic()) {
			failed++
		}
	}

	return called, failed
}

// callWithTimeout reports whether the call returned a non-nil error.
func callWithTimeout(method reflect.Value, args []reflect.Value, variadic bool) bool {
	done := make(chan bool, 1)

	go func() {
		failed := false

		defer func() {
			_ = recover()
			done <- failed
		}()

		var out []reflect.Value
		if variadic {
			out = method.CallSlice(args)
		} else {
			out = method.Call(args)
		}

		failed = !out[len(out)-1].IsNil()
	}()

	select {
	case failed := <-done:
		return failed
	case <-time.After(sweepCallTimeout):
		return false
	}
}

// placeholder builds a non-nil value of typ so a method gets past argument
// dereferences and reaches its first query.
func placeholder(ctx context.Context, typ reflect.Type) reflect.Value {
	switch {
	case typ == reflect.TypeFor[context.Context]():
		return reflect.ValueOf(ctx)
	case typ.Kind() == reflect.Pointer:
		return reflect.New(typ.Elem())
	case typ.Kind() == reflect.String:
		return reflect.ValueOf("x").Convert(typ)
	case typ.Kind() == reflect.Slice:
		return reflect.MakeSlice(typ, 0, 0)
	case typ.Kind() == reflect.Map:
		return reflect.MakeMap(typ)
	default:
		return reflect.Zero(typ)
	}
}
