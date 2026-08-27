package support_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/support"
)

// countMessages is the assertion the whole spec turns on.
//
// "The refusal stores nothing" is the claim most easily faked by a test that
// simply never looks at the store, so every refusal below asserts the actual
// row count rather than trusting the returned error.
func countMessages(t *testing.T, h *harness, threadUID string) int {
	t.Helper()

	messages, err := h.svc.ListMessages(t.Context(), threadUID, 0)
	require.NoError(t, err)

	return len(messages)
}

// TestReplyRouteFor_IsPerThreadNotPerChannel is the core regression: two
// threads on the SAME channel, one routable and one not, must give different
// answers.
//
// The bug this replaces asked a boot-time map whether an adapter existed and
// reported every Slack thread as answerable — including the workspace whose app
// was installed outside SolidPing and for which no bot token has ever been
// stored.
func TestReplyRouteFor_IsPerThreadNotPerChannel(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	// A stand-in for "is there a stored connection for this workspace?" — the
	// route func reads local state and never touches a provider.
	connected := map[string]bool{"T0ACME0001": true}

	h.svc.RegisterRoutedReplier(models.SupportChannelSlack,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			return "1724500000.000100", nil
		},
		func(_ context.Context, thread *models.SupportThread) support.ReplyRoute {
			teamID, _ := thread.ChannelContext["teamId"].(string)
			if !connected[teamID] {
				return support.ReplyRoute{Reason: "no stored connection for this Slack workspace"}
			}

			return support.ReplyRoute{CanReply: true}
		})

	routable, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSlack, Identity: "U0ACME1234",
		ExternalID: "slack:ok:1", Body: "hi",
		Context: map[string]any{"teamId": "T0ACME0001", "channelId": "D0ACME1111"},
	})
	r.NoError(err)

	orphan, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSlack, Identity: "U0ACME9999",
		ExternalID: "slack:orphan:1", Body: "hi",
		Context: map[string]any{"teamId": "T0ACME0002", "channelId": "D0ACME2222"},
	})
	r.NoError(err)

	// Same channel, same registered adapter, opposite answers.
	yes := h.svc.ReplyRouteFor(t.Context(), routable)
	r.True(yes.CanReply)
	r.Empty(yes.Reason)

	no := h.svc.ReplyRouteFor(t.Context(), orphan)
	r.False(no.CanReply)
	r.Contains(no.Reason, "no stored connection")
}

// TestReply_UnroutableIsRefusedAndStoresNothing walks the three channels the
// spec names, each with its own refusal and its own positive control.
//
// The pairing is the point: an assertion that a refusal stores nothing passes
// just as happily against a service that stores nothing ever, so each case also
// proves the routable twin DOES store a `sent` message.
func TestReply_UnroutableIsRefusedAndStoresNothing(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		channel string
		// routable is the channel context / attribution that resolves.
		routableContext map[string]any
		orphanContext   map[string]any
		// route is the adapter's pre-flight, written the way the real wiring
		// writes it for that channel.
		route  support.RouteFunc
		reason string
	}{
		{
			name:            "slack workspace with no stored connection",
			channel:         models.SupportChannelSlack,
			routableContext: map[string]any{"teamId": "T0ACME0001", "channelId": "D0ACME1111"},
			orphanContext:   map[string]any{"teamId": "T0ACME0002", "channelId": "D0ACME2222"},
			reason:          "holds no bot token",
			route: func(_ context.Context, thread *models.SupportThread) support.ReplyRoute {
				if teamID, _ := thread.ChannelContext["teamId"].(string); teamID != "T0ACME0001" {
					return support.ReplyRoute{Reason: "SolidPing holds no bot token for this workspace"}
				}

				return support.ReplyRoute{CanReply: true}
			},
		},
		{
			name:            "discord thread with no channel id",
			channel:         models.SupportChannelDiscord,
			routableContext: map[string]any{"channelId": "1122334455"},
			orphanContext:   map[string]any{},
			reason:          "no channel id",
			route: func(_ context.Context, thread *models.SupportThread) support.ReplyRoute {
				if channelID, _ := thread.ChannelContext["channelId"].(string); channelID == "" {
					return support.ReplyRoute{Reason: "this Discord thread carries no channel id"}
				}

				return support.ReplyRoute{CanReply: true}
			},
		},
		{
			name:            "sms with no sender configured",
			channel:         models.SupportChannelSMS,
			routableContext: map[string]any{"smsConfigured": true},
			orphanContext:   map[string]any{},
			reason:          "no SMS sender",
			route: func(_ context.Context, thread *models.SupportThread) support.ReplyRoute {
				if ok, _ := thread.ChannelContext["smsConfigured"].(bool); !ok {
					return support.ReplyRoute{Reason: "no SMS sender is available for this thread"}
				}

				return support.ReplyRoute{CanReply: true}
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			r := require.New(t)
			h := newHarness(t, "")

			sends := 0
			h.svc.RegisterRoutedReplier(testCase.channel,
				func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
					sends++

					return "provider-id-1", nil
				}, testCase.route)

			orphan, _, err := h.svc.Capture(t.Context(), &support.Inbound{
				Channel: testCase.channel, Identity: "orphan-identity",
				ExternalID: testCase.channel + ":orphan:1", Body: "help",
				Context: testCase.orphanContext,
			})
			r.NoError(err)

			// 1. The pre-flight says no, with a reason the operator can act on.
			route := h.svc.ReplyRouteFor(t.Context(), orphan)
			r.False(route.CanReply)
			r.Contains(route.Reason, testCase.reason)

			// 2. The API refuses too — the UI check is a courtesy, this is the
			//    rule — and carries the same reason.
			admin := newAdmin(t, h)

			msg, err := h.svc.Reply(t.Context(), orphan.UID, "we are on it", admin)
			r.ErrorIs(err, support.ErrNoReplyRoute)
			r.Contains(err.Error(), testCase.reason)
			r.Nil(msg)
			r.Zero(sends, "an unroutable reply must never reach the adapter")

			// 3. NOTHING WAS STORED. Only the inbound message is there: no send
			//    was attempted, so there is no attempt to record.
			r.Equal(1, countMessages(t, h, orphan.UID),
				"a refused reply must not leave an outbound row behind")

			after, err := h.svc.GetThread(t.Context(), orphan.UID)
			r.NoError(err)
			r.Equal(models.SupportStatusOpen, after.Status,
				"a refusal must not move the thread to pending — nobody answered")

			// POSITIVE CONTROL: the routable twin on the same channel and the
			// same adapter goes out and is stored as sent.
			routable, _, err := h.svc.Capture(t.Context(), &support.Inbound{
				Channel: testCase.channel, Identity: "routable-identity",
				ExternalID: testCase.channel + ":ok:1", Body: "help",
				Context: testCase.routableContext,
			})
			r.NoError(err)

			r.True(h.svc.ReplyRouteFor(t.Context(), routable).CanReply)

			sent, err := h.svc.Reply(t.Context(), routable.UID, "we are on it", admin)
			r.NoError(err)
			r.Equal("sent", sent.Delivery["status"])
			r.Equal(1, sends)
			r.Equal(2, countMessages(t, h, routable.UID))
		})
	}
}

// TestReply_SendFailureIsStillStored pins the OTHER half of the distinction.
//
// Unroutable (knowable in advance) refuses and stores nothing; a send that was
// actually attempted and rejected by the provider is still recorded with
// `Delivery failed`, exactly as before. Collapsing the two would either lose an
// operator's words or invent an attempt that never happened.
func TestReply_SendFailureIsStillStored(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	h.svc.RegisterRoutedReplier(models.SupportChannelSlack,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			return "", errProviderExploded
		},
		func(_ context.Context, _ *models.SupportThread) support.ReplyRoute {
			// Routable: the connection exists. The provider is simply down.
			return support.ReplyRoute{CanReply: true}
		})

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSlack, Identity: "U0ACME1234",
		ExternalID: "slack:down:1", Body: "hi",
		Context: map[string]any{"teamId": "T0ACME0001", "channelId": "D0ACME1111"},
	})
	r.NoError(err)

	msg, err := h.svc.Reply(t.Context(), thread.UID, "we see it too", newAdmin(t, h))
	r.Error(err)
	r.NotNil(msg, "an ATTEMPTED send that failed must still leave a trace")
	r.Equal("failed", msg.Delivery["status"])
	r.Equal(2, countMessages(t, h, thread.UID))
}

// TestReply_WhatsAppWindowStillRefusesFirst proves the existing window path is
// untouched, and that it runs BEFORE the routing pre-flight.
//
// Ordering matters for the message the operator reads: "the 24-hour window has
// lapsed" is actionable, "no route" would be both wrong and useless.
func TestReply_WhatsAppWindowStillRefusesFirst(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	routeCalls := 0
	sends := 0

	h.svc.RegisterRoutedReplier(models.SupportChannelWhatsApp,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			sends++

			return "wamid.OUT", nil
		},
		func(_ context.Context, _ *models.SupportThread) support.ReplyRoute {
			routeCalls++

			return support.ReplyRoute{CanReply: true}
		})

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelWhatsApp, Identity: "+33600000000",
		ExternalID: "wamid.IN", Body: "help",
	})
	r.NoError(err)

	h.now = h.now.Add(25 * time.Hour)

	_, err = h.svc.Reply(t.Context(), thread.UID, "still there?", newAdmin(t, h))
	r.ErrorIs(err, support.ErrReplyWindowClosed)
	r.NotErrorIs(err, support.ErrNoReplyRoute)
	r.Contains(err.Error(), "24-hour")
	r.Zero(routeCalls, "the window check must short-circuit before the routing pre-flight")
	r.Zero(sends)
	r.Equal(1, countMessages(t, h, thread.UID))
}

// TestResend_ReRunsThePreflightAndTheSend covers the operator's way out of text
// that is stored and unsent — including every reply queued against a Slack
// workspace before it was connected.
func TestResend_ReRunsThePreflightAndTheSend(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	// Starts broken on BOTH axes, exactly like the field case: the send fails,
	// and once it has, the connection is still missing.
	routable := false
	providerUp := false

	h.svc.RegisterRoutedReplier(models.SupportChannelSlack,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			if !providerUp {
				return "", errProviderExploded
			}

			return "1724500000.000200", nil
		},
		func(_ context.Context, _ *models.SupportThread) support.ReplyRoute {
			if !routable {
				return support.ReplyRoute{Reason: "SolidPing holds no bot token for this workspace"}
			}

			return support.ReplyRoute{CanReply: true}
		})

	thread, _, err := h.svc.Capture(t.Context(), &support.Inbound{
		Channel: models.SupportChannelSlack, Identity: "U0ACME1234",
		ExternalID: "slack:resend:1", Body: "hi",
		Context: map[string]any{"teamId": "T0ACME0001", "channelId": "D0ACME1111"},
	})
	r.NoError(err)

	admin := newAdmin(t, h)

	// A routable-but-failing send: the row exists, flagged failed. This is the
	// state the whole resend endpoint is for.
	routable = true

	failed, err := h.svc.Reply(t.Context(), thread.UID, "we are on it", admin)
	r.Error(err)
	r.Equal("failed", failed.Delivery["status"])

	// Still unroutable → refused, and the stored row is left exactly as it was
	// rather than being rewritten with a misleading second failure.
	routable = false

	_, err = h.svc.Resend(t.Context(), thread.UID, failed.UID)
	r.ErrorIs(err, support.ErrNoReplyRoute)

	// Route restored, provider still down: attempted, so the failure IS
	// re-recorded with the fresh provider error.
	routable = true

	retried, err := h.svc.Resend(t.Context(), thread.UID, failed.UID)
	r.Error(err)
	r.NotNil(retried)
	r.Equal("failed", retried.Delivery["status"])

	// Everything healthy: it goes, in place, without appending a duplicate.
	providerUp = true

	sent, err := h.svc.Resend(t.Context(), thread.UID, failed.UID)
	r.NoError(err)
	r.Equal(failed.UID, sent.UID, "resend rewrites the row, it does not append a copy")
	r.Equal("sent", sent.Delivery["status"])
	r.NotNil(sent.ExternalID)
	r.Equal("1724500000.000200", *sent.ExternalID)
	r.Equal(2, countMessages(t, h, thread.UID))

	stored, err := h.svc.ListMessages(t.Context(), thread.UID, 0)
	r.NoError(err)

	for _, msg := range stored {
		if msg.UID == failed.UID {
			r.Equal("sent", msg.Delivery["status"], "the rewrite must be persisted, not just returned")
		}
	}

	// A delivered reply is NOT resendable: doing it would send the customer the
	// same text twice.
	_, err = h.svc.Resend(t.Context(), thread.UID, failed.UID)
	r.ErrorIs(err, support.ErrNotResendable)

	// Neither is an inbound message, nor a uid from another conversation.
	inbound, err := h.svc.ListMessages(t.Context(), thread.UID, 0)
	r.NoError(err)

	for _, msg := range inbound {
		if msg.Direction == models.SupportDirectionInbound {
			_, err = h.svc.Resend(t.Context(), thread.UID, msg.UID)
			r.ErrorIs(err, support.ErrNotResendable)
		}
	}

	_, err = h.svc.Resend(t.Context(), thread.UID, "00000000-0000-0000-0000-000000000000")
	r.ErrorIs(err, support.ErrMessageNotFound)
}

// TestReplyRoutes_MemoizesPerRoutingInputs pins the listing path: an inbox page
// must not turn one connection lookup into one per row.
func TestReplyRoutes_MemoizesPerRoutingInputs(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	h := newHarness(t, "")

	lookups := 0

	h.svc.RegisterRoutedReplier(models.SupportChannelSlack,
		func(_ context.Context, _ *models.SupportThread, _ string) (string, error) {
			return "ts", nil
		},
		func(_ context.Context, thread *models.SupportThread) support.ReplyRoute {
			lookups++

			if teamID, _ := thread.ChannelContext["teamId"].(string); teamID == "T0ACME0001" {
				return support.ReplyRoute{CanReply: true}
			}

			return support.ReplyRoute{Reason: "no stored connection"}
		})

	// Three senders in the connected workspace, one in the orphaned one.
	for index, ctxMap := range []map[string]any{
		{"teamId": "T0ACME0001", "channelId": "D1"},
		{"teamId": "T0ACME0001", "channelId": "D1"},
		{"teamId": "T0ACME0001", "channelId": "D1"},
		{"teamId": "T0ACME0002", "channelId": "D9"},
	} {
		_, _, err := h.svc.Capture(t.Context(), &support.Inbound{
			Channel: models.SupportChannelSlack, Identity: "U" + string(rune('A'+index)),
			ExternalID: "slack:list:" + string(rune('A'+index)), Body: "hi", Context: ctxMap,
		})
		r.NoError(err)
	}

	threads, err := h.svc.ListThreads(t.Context(), models.ListSupportThreadsFilter{})
	r.NoError(err)
	r.Len(threads, 4)

	routes := h.svc.ReplyRoutes(t.Context(), threads)
	r.Len(routes, 4)
	r.Equal(2, lookups, "identical routing inputs must resolve once, not once per thread")

	answered := 0

	for _, route := range routes {
		if route.CanReply {
			answered++
		}
	}

	r.Equal(3, answered)
}
