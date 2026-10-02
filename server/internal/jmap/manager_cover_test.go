package jmap_test

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

func newCoverDB(t *testing.T) *sqlite.Service {
	t.Helper()

	dbSvc, err := sqlite.New(t.Context(), sqlite.Config{InMemory: true})
	require.NoError(t, err)
	require.NoError(t, dbSvc.Initialize(t.Context()))
	t.Cleanup(func() { _ = dbSvc.Close() })

	return dbSvc
}

func storeConfig(t *testing.T, dbSvc *sqlite.Service, cfg *jmap.Config) {
	t.Helper()

	raw, err := json.Marshal(cfg)
	require.NoError(t, err)

	var asMap map[string]any
	require.NoError(t, json.Unmarshal(raw, &asMap))

	require.NoError(t, dbSvc.SetSystemParameter(t.Context(), jmap.SystemParameterKey, asMap, true))
}

func TestManagerRunPollsAndProcessesInbox(t *testing.T) {
	t.Parallel()

	fake := newFakeMailbox(t)
	seedInboxMessage(fake, "<run@example.com>")

	dbSvc := newCoverDB(t)
	storeConfig(t, dbSvc, fake.config())

	var recorded atomic.Int32

	mgr := jmap.NewManager(dbSvc)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- mgr.Run(ctx) }()

	require.Eventually(t, func() bool { return recorded.Load() == 1 }, 10*time.Second, 20*time.Millisecond)
	require.Eventually(t, func() bool { return mgr.GetStatus().Connected }, 5*time.Second, 20*time.Millisecond)

	status := mgr.GetStatus()
	require.True(t, status.Enabled)
	require.Equal(t, "poll", status.Mode)
	require.Equal(t, "acc-1", status.AccountID)
	require.Equal(t, "inbox.example.com", status.AddressDomain)
	require.Equal(t, fakeProcessedID, fake.mailboxOf("e1"))

	// A trigger on a live manager is accepted and coalesces.
	require.NoError(t, mgr.TriggerSync(ctx))
	require.NoError(t, mgr.TriggerSync(ctx))

	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}

	require.False(t, mgr.GetStatus().Connected)
}

func TestManagerRunIdlesWithoutUsableConfig(t *testing.T) {
	t.Parallel()

	dbSvc := newCoverDB(t)
	mgr := jmap.NewManager(dbSvc)

	// No config stored: Run idles until the context ends.
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	require.Error(t, mgr.Run(ctx))
	require.False(t, mgr.GetStatus().Connected)

	// An enabled but invalid config is recorded as the last error.
	storeConfig(t, dbSvc, &jmap.Config{Enabled: true})

	ctx2, cancel2 := context.WithTimeout(t.Context(), 300*time.Millisecond)
	defer cancel2()

	require.Error(t, mgr.Run(ctx2))
	require.Contains(t, mgr.GetStatus().LastError, "sessionUrl")
}

func TestManagerTestConnection(t *testing.T) {
	t.Parallel()

	fake := newFakeMailbox(t)
	dbSvc := newCoverDB(t)
	mgr := jmap.NewManager(dbSvc)

	// Explicit config.
	mboxes, err := mgr.TestConnection(t.Context(), fake.config())
	require.NoError(t, err)
	require.Equal(t, fakeInboxID, mboxes.Inbox.ID)
	require.Equal(t, fakeProcessedID, mboxes.Processed.ID)

	// Config loaded from the system parameter.
	storeConfig(t, dbSvc, fake.config())

	mboxes, err = mgr.TestConnection(t.Context(), nil)
	require.NoError(t, err)
	require.NotNil(t, mboxes)

	// Validation failures.
	_, err = mgr.TestConnection(t.Context(), &jmap.Config{})
	require.ErrorIs(t, err, jmap.ErrSessionURLRequired)

	_, err = mgr.TestConnection(t.Context(), &jmap.Config{SessionURL: "http://x"})
	require.ErrorIs(t, err, jmap.ErrAddressDomainRequired)

	// Unreachable server.
	_, err = mgr.TestConnection(t.Context(), &jmap.Config{
		SessionURL: "http://127.0.0.1:1/.well-known/jmap", AddressDomain: "x.acme.com",
	})
	require.Error(t, err)
}

func TestManagerCleanupOldEmails(t *testing.T) {
	t.Parallel()

	fake := newFakeMailbox(t)
	client := fake.client(t)
	cfg := fake.config()
	mgr := jmap.NewManager(newCoverDB(t))

	// Without a Trash mailbox cleanup is a no-op.
	require.NoError(t, mgr.CleanupOldEmailsForTest(t.Context(), client, fakeMailboxes(), cfg))

	old := time.Now().Add(-90 * 24 * time.Hour).UTC()
	fake.put("old-processed", &fakeEmail{mailbox: fakeProcessedID, to: "a@x", messageID: "<1>", receivedAt: old})
	fake.put("old-inbox", &fakeEmail{mailbox: fakeInboxID, to: "b@x", messageID: "<2>", receivedAt: old})

	mboxes := fakeMailboxes()
	mboxes.Trash = &jmap.Mailbox{ID: "trash", Name: "Trash"}

	require.NoError(t, mgr.CleanupOldEmailsForTest(t.Context(), client, mboxes, cfg))

	require.Equal(t, "trash", fake.mailboxOf("old-processed"))
	require.Equal(t, "trash", fake.mailboxOf("old-inbox"))
}

func TestManagerStatusModeAndHelpers(t *testing.T) {
	t.Parallel()

	mgr := jmap.NewManager(newCoverDB(t))

	mgr.SetModeForTest("push")
	require.Empty(t, mgr.GetStatus().Mode, "mode is hidden while disconnected")

	mgr.SetConnectedForTest(true)
	require.Equal(t, "push", mgr.GetStatus().Mode)

	// extractEmailState
	state, ok := jmap.ExtractEmailStateForTest(
		jmap.EventSourceEvent{Type: "state", Data: `{"changed":{"acc-1":{"Email":"s7"}}}`}, "acc-1")
	require.True(t, ok)
	require.Equal(t, "s7", state)

	for name, ev := range map[string]jmap.EventSourceEvent{
		"wrong type":  {Type: "ping", Data: `{}`},
		"bad json":    {Type: "state", Data: `{`},
		"other acct":  {Type: "state", Data: `{"changed":{"zzz":{"Email":"s"}}}`},
		"no email":    {Type: "state", Data: `{"changed":{"acc-1":{"Mailbox":"s"}}}`},
		"empty state": {Type: "state", Data: `{}`},
	} {
		_, ok := jmap.ExtractEmailStateForTest(ev, "acc-1")
		require.False(t, ok, name)
	}

	// sleepOrDone
	require.True(t, jmap.SleepOrDoneForTest(t.Context(), time.Millisecond))

	canceled, cancel := context.WithCancel(t.Context())
	cancel()

	require.False(t, jmap.SleepOrDoneForTest(canceled, time.Hour))
}

func TestClientAccessorsAndMethodsMisc(t *testing.T) {
	t.Parallel()

	fake := newFakeMailbox(t)
	client := fake.client(t)

	require.NotNil(t, client.Session())
	client.SetRewriteBaseURL("http://proxy.acme.com")

	props := jmap.DefaultEmailProperties()
	require.NotEmpty(t, props)

	props[0] = "mutated"
	require.NotEqual(t, "mutated", jmap.DefaultEmailProperties()[0], "returns a copy")

	// The fake does not implement Email/changes: the error must surface.
	_, err := client.EmailChanges(t.Context(), "acc-1", "s0")
	require.Error(t, err)

	raw, err := json.Marshal(jmap.MethodResponse{Name: "X/get", Args: json.RawMessage(`{"a":1}`), ClientID: "c0"})
	require.NoError(t, err)
	require.JSONEq(t, `["X/get",{"a":1},"c0"]`, string(raw))

	_ = models.JSONMap{}
}
