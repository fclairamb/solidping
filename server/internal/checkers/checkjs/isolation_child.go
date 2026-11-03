package checkjs

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"strings"
	"time"

	"github.com/dop251/goja"
)

const (
	// childWatchInterval is how often the child's watchdog reads its heap.
	childWatchInterval = 2 * time.Millisecond
	// childGCPercent is the share of the limit the Go GC aims under.
	childGCPercent = 80
	// childMaxProcs keeps a child to a couple of threads.
	childMaxProcs = 2
	// percent converts childGCPercent.
	percent = 100
)

// childExtraGlobals lets a test binary add globals to its children.
//
//nolint:gochecknoglobals // Test hook, set before init runs.
var childExtraGlobals func(r *jsRuntime)

// The hidden subcommand: a binary linking this package started with
// SP_JS_CHILD=1 runs one script and exits, before any other init of the
// program (its main, its config) runs. It may also run before this
// package's other inits: whatever a script needs is set by a package
// variable, never by an init.
//
//nolint:gochecknoinits // The child entry point must run before main.
func init() {
	if os.Getenv(childEnvVar) != "1" {
		return
	}

	os.Exit(runChildProcess(os.Stdin, os.Stdout))
}

// childChannel is the child's side of the channel.
type childChannel struct {
	in  *bufio.Reader
	out io.Writer
	// stringify is the capped JSON.stringify, read before the script runs:
	// a script that replaces JSON.stringify cannot hand the parent arguments
	// it did not pay for (spec 2026-11-03-01).
	stringify goja.Callable
}

// runChildProcess runs the script init describes and writes its result.
func runChildProcess(stdin io.Reader, stdout io.Writer) int {
	// Stray prints go to stderr: stdout carries the channel only.
	os.Stdout = os.Stderr

	channel := &childChannel{in: bufio.NewReader(stdin), out: stdout}

	msg, err := readFrame(channel.in, maxParentFrameBytes)
	if err != nil || msg.Init == nil {
		fmt.Fprintln(os.Stderr, "solidping js child: no init message:", err)

		return 1
	}

	setup := msg.Init

	limitChildMemory(setup.MemoryLimit)

	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, setup.DeadlineNs))
	defer cancel()

	pluginConfig, _ := normalizeNumbers(setup.PluginConfig).(map[string]any)
	if setup.Plugin && pluginConfig == nil {
		pluginConfig = map[string]any{}
	}

	cfg := &JSConfig{
		Script:       setup.Script,
		Env:          setup.Env,
		Secrets:      setup.Secrets,
		PluginConfig: pluginConfig,
		Timeout:      time.Duration(setup.TimeoutNs),
	}

	start := time.Now()
	jsr := newJSRuntime(ctx, cfg)
	jsr.registerLocalGlobals()

	channel.stringify, _ = goja.AssertFunction(jsr.vm.Get("JSON").ToObject(jsr.vm).Get("stringify"))

	for _, name := range hostGlobals {
		if node, ok := setup.Globals[name]; ok {
			_ = jsr.vm.Set(name, channel.decode(jsr, node))
		}
	}

	if childExtraGlobals != nil {
		childExtraGlobals(jsr)
	}

	jsr.guardBindings()

	result := jsr.runScript(start, cfg.Timeout)

	done := &wireResult{
		Status:     result.Status.String(),
		DurationNs: int64(result.Duration),
		Output:     result.Output,
		Metrics:    result.Metrics,
	}

	if err := writeFrame(channel.out, &wireMsg{Done: done}); err != nil {
		fmt.Fprintln(os.Stderr, "solidping js child: writing the result:", err)

		return 1
	}

	return 0
}

// limitChildMemory caps the child: a hard limit where the platform has one
// (rlimit on Linux), the Go GC aiming under the limit, and a watchdog that
// exits with childExitMemory once the heap passes it.
func limitChildMemory(limit int64) {
	if limit <= 0 {
		limit = defaultChildMemoryLimit
	}

	runtime.GOMAXPROCS(childMaxProcs)
	debug.SetMemoryLimit(limit * childGCPercent / percent)

	if err := setChildRlimit(limit); err != nil {
		fmt.Fprintln(os.Stderr, "solidping js child: rlimit:", err)
	}

	go watchChildMemory(limit)
}

// watchChildMemory exits the process once its heap passes limit. goja cannot
// interrupt a native call, so this is what stops one that allocates.
func watchChildMemory(limit int64) {
	samples := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}

	for {
		metrics.Read(samples)

		heap := samples[0].Value
		if heap.Kind() == metrics.KindUint64 && heap.Uint64() > uint64(limit) {
			fmt.Fprintf(os.Stderr, "solidping js child: out of memory: heap over the %d MiB limit\n", limit/bytesPerMiB)
			os.Exit(childExitMemory)
		}

		time.Sleep(childWatchInterval)
	}
}

// roundTrip sends msg and reads the parent's answer.
func (c *childChannel) roundTrip(msg *wireMsg) (*wireMsg, error) {
	if err := writeFrame(c.out, msg); err != nil {
		return nil, err
	}

	return readFrame(c.in, maxParentFrameBytes)
}

// decode builds the child-side value of a node.
func (c *childChannel) decode(jsr *jsRuntime, node *wireNode) goja.Value {
	if node == nil {
		return goja.Undefined()
	}

	switch node.T {
	case nodeValue:
		val, err := decodeJSONValue(node.V)
		if err != nil {
			panic(jsr.vm.NewGoError(err))
		}

		return jsr.vm.ToValue(val)
	case nodeFunction:
		return jsr.vm.ToValue(c.stub(jsr, node.ID))
	case nodeArray:
		items := make([]any, 0, len(node.Values))
		for _, inner := range node.Values {
			items = append(items, c.decode(jsr, inner))
		}

		return jsr.vm.NewArray(items...)
	case nodeObject:
		obj := jsr.vm.NewObject()

		for i, key := range node.Keys {
			if i < len(node.Values) {
				_ = obj.Set(key, c.decode(jsr, node.Values[i]))
			}
		}

		return obj
	default:
		return goja.Undefined()
	}
}

// stub is the child-side function that calls a served one.
func (c *childChannel) stub(jsr *jsRuntime, id int) func(goja.FunctionCall) goja.Value {
	return func(call goja.FunctionCall) goja.Value {
		args, undef := c.encodeArgs(jsr, call.Arguments)

		reply, err := c.roundTrip(&wireMsg{Call: &wireCall{ID: id, Args: args, Undef: undef}})
		if err != nil {
			panic(jsr.vm.NewGoError(fmt.Errorf("the check process did not answer: %w", err)))
		}

		if reply.Err != nil {
			panic(c.thrown(jsr, reply.Err))
		}

		return c.decode(jsr, reply.Ret)
	}
}

// encodeArgs writes the arguments with the capped JSON.stringify captured
// before the script ran, so they draw from the memory budget whatever the
// script did to the global JSON.
func (c *childChannel) encodeArgs(r *jsRuntime, args []goja.Value) (string, []int) {
	stringify := c.stringify
	if stringify == nil {
		panic(r.vm.NewGoError(errChildProtocol))
	}

	var (
		out   strings.Builder
		undef []int
	)

	out.WriteByte('[')

	for i, arg := range args {
		if i > 0 {
			out.WriteByte(',')
		}

		text := callOriginal(stringify, goja.Undefined(), arg)
		if goja.IsUndefined(text) {
			undef = append(undef, i)

			out.WriteString("null")

			continue
		}

		out.WriteString(text.String())
	}

	out.WriteByte(']')

	return out.String(), undef
}

// standardErrors are the constructors a served throw is rebuilt with.
//
//nolint:gochecknoglobals // Fixed list.
var standardErrors = map[string]bool{
	"Error": true, "RangeError": true, "TypeError": true, "SyntaxError": true,
	"ReferenceError": true, "URIError": true, "EvalError": true,
}

// thrown rebuilds what a served call threw.
func (c *childChannel) thrown(jsr *jsRuntime, thrown *wireErr) goja.Value {
	if thrown.Value != nil {
		return c.decode(jsr, thrown.Value)
	}

	if standardErrors[thrown.Name] {
		if ctor, ok := goja.AssertConstructor(jsr.vm.Get(thrown.Name)); ok {
			if obj, err := ctor(nil, jsr.vm.ToValue(thrown.Message)); err == nil {
				return obj
			}
		}
	}

	return jsr.vm.NewGoError(errors.New(thrown.Message)) //nolint:err113 // Rebuilt from the parent's message.
}
