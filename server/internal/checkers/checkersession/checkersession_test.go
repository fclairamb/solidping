package checkersession

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
)

var (
	errTestSlotTimeout  = NewSlotTimeoutError("timed out waiting for a free test slot")
	errTestInfra        = errors.New("the session is gone")
	errTestTarget       = errors.New("the change never came")
	errTestUnknownShape = errors.New("unknown shape")
)

type declaredInfraError struct{ infra bool }

func (d *declaredInfraError) Error() string { return "declared" }
func (d *declaredInfraError) Infra() bool   { return d.infra }

func TestSlotsCapAndRelease(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	slots := NewSlots(2, errTestSlotTimeout)

	first, err := slots.Acquire(t.Context())
	r.NoError(err)

	second, err := slots.Acquire(t.Context())
	r.NoError(err)
	r.Equal(2, slots.InUse())
	r.Equal(2, slots.Capacity())

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err = slots.Acquire(ctx)
	r.ErrorIs(err, errTestSlotTimeout, "a full semaphore must give up with the checker's own error")
	r.ErrorIs(err, ErrSlotTimeout, "and that error must match the shared sentinel")
	r.GreaterOrEqual(time.Since(start), 50*time.Millisecond, "it must actually have waited")

	first()
	first() // idempotent: a double release must not free a slot someone else holds
	r.Equal(1, slots.InUse())

	second()
	r.Zero(slots.InUse())
}

func TestSlotTimeoutResult(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	output := map[string]any{}
	result, ok := SlotTimeoutResult(fmt.Errorf("open: %w", errTestSlotTimeout), time.Now(), map[string]any{}, output)
	r.True(ok)
	r.Equal(checkerdef.StatusTimeout, result.Status)
	r.Contains(output[checkerdef.OutputKeyError], "test slot")

	_, ok = SlotTimeoutResult(errTestInfra, time.Now(), map[string]any{}, map[string]any{})
	r.False(ok, "any other error is not a slot timeout")
}

func TestClassifierOrder(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fallbackCalls := 0
	classifier := Classifier{
		InfraSentinels:  []error{errTestInfra},
		TargetSentinels: []error{errTestTarget},
		Fallback: func(error) bool {
			fallbackCalls++

			return true
		},
	}

	r.False(classifier.Infra(nil))
	r.True(classifier.Infra(fmt.Errorf("click: %w", errTestInfra)))
	r.False(classifier.Infra(errTestTarget))
	r.False(classifier.Infra(errTestSlotTimeout), "a full worker is not a broken one")
	r.False(classifier.Infra(context.DeadlineExceeded), "the check's own timeout is not infra")
	r.False(classifier.Infra(context.Canceled))
	r.True(classifier.Infra(&declaredInfraError{infra: true}))
	r.False(classifier.Infra(fmt.Errorf("wrapped: %w", &declaredInfraError{infra: false})),
		"a type that declares itself target-side wins over the fallback")
	r.Zero(fallbackCalls, "every error above was decided before the fallback")

	r.True(classifier.Infra(errTestUnknownShape))
	r.Equal(1, fallbackCalls)

	r.False(Classifier{}.Infra(errTestUnknownShape), "no fallback means target-side")
}
