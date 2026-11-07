//go:build windows

package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/windows/svc"
)

var errServeBoom = errors.New("serve boom")

// executeHandler runs handler.Execute in the background and returns the
// request channel, the status channel and a channel carrying its result.
func executeHandler(handler *serviceHandler) (chan svc.ChangeRequest, chan svc.Status, chan [2]any) {
	requests := make(chan svc.ChangeRequest)
	status := make(chan svc.Status, 8)
	result := make(chan [2]any, 1)

	go func() {
		specific, code := handler.Execute(nil, requests, status)
		result <- [2]any{specific, code}
	}()

	return requests, status, result
}

func TestServiceHandlerStopCancelsRun(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := &serviceHandler{run: func(ctx context.Context) error {
		<-ctx.Done()

		return nil
	}}
	requests, status, result := executeHandler(handler)

	r.Equal(svc.StartPending, (<-status).State)
	r.Equal(svc.Running, (<-status).State)

	requests <- svc.ChangeRequest{Cmd: svc.Stop}

	r.Equal(svc.StopPending, (<-status).State)
	r.Equal([2]any{false, uint32(0)}, <-result)
	r.NoError(handler.err)
}

func TestServiceHandlerRunFailureExitsWithError(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	handler := &serviceHandler{run: func(context.Context) error { return errServeBoom }}
	_, _, result := executeHandler(handler)

	select {
	case got := <-result:
		r.Equal([2]any{true, uint32(1)}, got)
	case <-time.After(5 * time.Second):
		t.Fatal("Execute did not return after run failed")
	}

	r.ErrorIs(handler.err, errServeBoom)
}
