package jmap_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/jmap"
)

const (
	fakeInboxID     = "inbox"
	fakeProcessedID = "processed"
)

// fakeMailbox is a JMAP server with REAL mailbox state: an Email/set move
// succeeds only if the message is still in the mailbox the patch removes it
// from. That compare-and-swap is the whole point — a fake that blindly
// acknowledges every move would let a broken claim implementation pass, since
// every racing consumer would believe it won.
type fakeMailbox struct {
	srv *httptest.Server

	mu     sync.Mutex
	emails map[string]*fakeEmail

	// rejectMoves makes every Email/set update reply notUpdated, reproducing
	// the mail provider refusing the archive.
	rejectMoves atomic.Bool

	// setBarrier, when non-nil, holds every Email/set caller until the
	// expected number of them has arrived, so two consumers are forced to
	// contend for the same message instead of politely taking turns.
	setBarrier *barrier

	setCalls atomic.Int32
}

type fakeEmail struct {
	mailbox    string
	to         string
	messageID  string
	subject    string
	receivedAt time.Time
}

// barrier releases its waiters once n of them have arrived.
type barrier struct {
	mu      sync.Mutex
	n       int
	arrived int
	ch      chan struct{}
}

func newBarrier(n int) *barrier {
	return &barrier{n: n, ch: make(chan struct{})}
}

func (b *barrier) wait() {
	b.mu.Lock()

	b.arrived++
	if b.arrived >= b.n {
		select {
		case <-b.ch:
		default:
			close(b.ch)
		}
	}

	b.mu.Unlock()

	select {
	case <-b.ch:
	case <-time.After(5 * time.Second):
	}
}

func newFakeMailbox(t *testing.T) *fakeMailbox {
	t.Helper()

	f := &fakeMailbox{emails: map[string]*fakeEmail{}}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/jmap", f.handleSession)
	mux.HandleFunc("/jmap", f.handleJMAP)
	f.srv = httptest.NewServer(mux)

	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeMailbox) put(id string, e *fakeEmail) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.emails[id] = e
}

func (f *fakeMailbox) mailboxOf(id string) string {
	f.mu.Lock()
	defer f.mu.Unlock()

	if e, ok := f.emails[id]; ok {
		return e.mailbox
	}

	return ""
}

func (f *fakeMailbox) config() *jmap.Config {
	cfg := &jmap.Config{
		Enabled:       true,
		SessionURL:    f.srv.URL + "/.well-known/jmap",
		Username:      "u",
		Password:      "p",
		AddressDomain: "inbox.example.com",
	}
	cfg.ApplyDefaults()

	return cfg
}

func (f *fakeMailbox) client(t *testing.T) *jmap.Client {
	t.Helper()

	client := jmap.NewClient(f.config())
	_, err := client.DiscoverSession(t.Context())
	require.NoError(t, err)

	return client
}

func fakeMailboxes() *jmap.Mailboxes {
	return &jmap.Mailboxes{
		Inbox:     &jmap.Mailbox{ID: fakeInboxID, Name: "Inbox"},
		Processed: &jmap.Mailbox{ID: fakeProcessedID, Name: "Processed"},
	}
}

func (f *fakeMailbox) handleSession(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"capabilities":{` +
		`"urn:ietf:params:jmap:core":{},"urn:ietf:params:jmap:mail":{}},` +
		`"accounts":{"acc-1":{"name":"x","isPersonal":true}},` +
		`"primaryAccounts":{"urn:ietf:params:jmap:mail":"acc-1"},` +
		`"apiUrl":"` + f.srv.URL + `/jmap","state":"s"}`))
}

type rawCall [3]json.RawMessage

func (f *fakeMailbox) handleJMAP(w http.ResponseWriter, req *http.Request) {
	body, _ := io.ReadAll(req.Body)

	var envelope struct {
		MethodCalls []rawCall `json:"methodCalls"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || len(envelope.MethodCalls) == 0 {
		w.WriteHeader(http.StatusBadRequest)

		return
	}

	var name string
	_ = json.Unmarshal(envelope.MethodCalls[0][0], &name)

	w.Header().Set("Content-Type", "application/json")

	switch name {
	case "Mailbox/get":
		f.writeResponse(w, "Mailbox/get", map[string]any{"list": []map[string]any{
			{"id": fakeInboxID, "name": "Inbox", "role": "inbox"},
			{"id": fakeProcessedID, "name": "Processed"},
		}})
	case "Email/query":
		f.handleQuery(w, envelope.MethodCalls[0][1])
	case "Email/get":
		f.handleGet(w, envelope.MethodCalls[0][1])
	case "Email/set":
		f.handleSet(w, envelope.MethodCalls[0][1])
	default:
		w.WriteHeader(http.StatusNotImplemented)
	}
}

func (f *fakeMailbox) writeResponse(w http.ResponseWriter, name string, args map[string]any) {
	payload, err := json.Marshal(map[string]any{
		"methodResponses": []any{[]any{name, args, "c0"}},
		"sessionState":    "s",
	})
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)

		return
	}

	_, _ = w.Write(payload)
}

func (f *fakeMailbox) handleQuery(w http.ResponseWriter, rawArgs json.RawMessage) {
	var args struct {
		Filter struct {
			InMailbox string `json:"inMailbox"`
			After     string `json:"after"`
		} `json:"filter"`
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(rawArgs, &args)

	var after time.Time
	if args.Filter.After != "" {
		after, _ = time.Parse(time.RFC3339, args.Filter.After)
	}

	f.mu.Lock()

	type entry struct {
		id string
		at time.Time
	}

	matches := make([]entry, 0, len(f.emails))

	for id, e := range f.emails {
		if e.mailbox != args.Filter.InMailbox {
			continue
		}

		if !after.IsZero() && e.receivedAt.Before(after) {
			continue
		}

		matches = append(matches, entry{id: id, at: e.receivedAt})
	}

	f.mu.Unlock()

	// Newest first, exactly as EmailQuery asks for.
	sort.Slice(matches, func(i, j int) bool { return matches[i].at.After(matches[j].at) })

	if args.Limit > 0 && len(matches) > args.Limit {
		matches = matches[:args.Limit]
	}

	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m.id)
	}

	f.writeResponse(w, "Email/query", map[string]any{"ids": ids})
}

func (f *fakeMailbox) handleGet(w http.ResponseWriter, rawArgs json.RawMessage) {
	var args struct {
		IDs []string `json:"ids"`
	}
	_ = json.Unmarshal(rawArgs, &args)

	f.mu.Lock()

	list := make([]map[string]any, 0, len(args.IDs))

	for _, id := range args.IDs {
		e, ok := f.emails[id]
		if !ok {
			continue
		}

		list = append(list, map[string]any{
			"id":         id,
			"receivedAt": e.receivedAt.UTC().Format(time.RFC3339),
			"from":       []map[string]any{{"email": "sender@example.com"}},
			"to":         []map[string]any{{"email": e.to}},
			"subject":    e.subject,
			"messageId":  []string{e.messageID},
			"mailboxIds": map[string]bool{e.mailbox: true},
		})
	}

	f.mu.Unlock()

	f.writeResponse(w, "Email/get", map[string]any{"list": list})
}

// handleSet implements the move as a compare-and-swap on the current mailbox,
// which is what makes a racing claim resolvable at all.
func (f *fakeMailbox) handleSet(w http.ResponseWriter, rawArgs json.RawMessage) {
	f.setCalls.Add(1)

	if f.setBarrier != nil {
		f.setBarrier.wait()
	}

	var args struct {
		Update map[string]map[string]any `json:"update"`
	}
	_ = json.Unmarshal(rawArgs, &args)

	updated := map[string]any{}
	notUpdated := map[string]any{}

	reject := f.rejectMoves.Load()

	f.mu.Lock()

	for id, patch := range args.Update {
		move := parsePatch(patch)

		email, ok := f.emails[id]
		if reject || !ok || email.mailbox != move.from {
			notUpdated[id] = map[string]any{"type": "notFound"}

			continue
		}

		email.mailbox = move.to
		updated[id] = nil
	}

	f.mu.Unlock()

	f.writeResponse(w, "Email/set", map[string]any{
		"updated": updated, "notUpdated": notUpdated,
	})
}

// mailboxMove is the (target, source) pair encoded in an RFC 8621 §4.6
// mailboxIds patch.
type mailboxMove struct {
	to   string
	from string
}

// parsePatch reads the patch back into the move it expresses.
func parsePatch(patch map[string]any) mailboxMove {
	const prefix = "mailboxIds/"

	var move mailboxMove

	for key, value := range patch {
		if len(key) <= len(prefix) || key[:len(prefix)] != prefix {
			continue
		}

		if value == nil {
			move.from = key[len(prefix):]

			continue
		}

		move.to = key[len(prefix):]
	}

	return move
}

// recordingHandler stands in for emailcheck: it claims token-shaped mail and
// counts a "result" per email it is asked to handle. The counter is shared
// between managers on purpose — it is the stand-in for the one results table
// every replica writes to.
type recordingHandler struct {
	recorded *atomic.Int32
	seen     *sync.Map // messageID -> struct{}, the layer-3 stand-in
	fail     bool
}

func (h *recordingHandler) ClaimsEmail(email jmap.Email) bool {
	return len(email.To) > 0 && email.To[0].Email != ""
}

func (h *recordingHandler) HandleEmail(
	_ context.Context, _ *jmap.Mailboxes, email jmap.Email,
) (jmap.Outcome, error) {
	if h.fail {
		return jmap.OutcomeIgnored, errHandlerBoom
	}

	if h.seen != nil && len(email.MessageID) > 0 {
		if _, dup := h.seen.LoadOrStore(email.MessageID[0], struct{}{}); dup {
			return jmap.OutcomeProcessed, nil
		}
	}

	h.recorded.Add(1)

	return jmap.OutcomeProcessed, nil
}

func seedInboxMessage(f *fakeMailbox, messageID string) {
	f.put("e1", &fakeEmail{
		mailbox:    fakeInboxID,
		to:         "token@inbox.example.com",
		messageID:  messageID,
		subject:    "ping",
		receivedAt: time.Now().UTC(),
	})
}

// TestLoneConsumerRecordsExactlyOne is the positive control for the race test
// below: it proves the harness actually delivers a message, so a "we recorded
// nothing" bug cannot masquerade as "no duplicates".
func TestLoneConsumerRecordsExactlyOne(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	seedInboxMessage(fake, "<solo@example.com>")

	var recorded atomic.Int32

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded})

	client := fake.client(t)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, fakeMailboxes(), fake.config()))

	r.Equal(int32(1), recorded.Load(), "a single consumer must record the email exactly once")
	r.Equal(fakeProcessedID, fake.mailboxOf("e1"), "the claimed message ends up in Processed")
}

// TestTwoConsumersRecordExactlyOnce is the regression test for the reported
// bug: two replicas polling one mailbox minted a result each. Both managers
// are held at the Email/set barrier so they contend for the same message; only
// the one whose move lands may record it.
func TestTwoConsumersRecordExactlyOnce(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	fake.setBarrier = newBarrier(2)
	seedInboxMessage(fake, "<race@example.com>")

	var recorded atomic.Int32

	managers := make([]*jmap.Manager, 2)
	clients := make([]*jmap.Client, 2)

	for i := range managers {
		managers[i] = jmap.NewManager(nil)
		managers[i].RegisterHandler(&recordingHandler{recorded: &recorded})
		clients[i] = fake.client(t)
	}

	var wg sync.WaitGroup

	errs := make([]error, len(managers))

	for i := range managers {
		wg.Add(1)

		go func() {
			defer wg.Done()

			errs[i] = managers[i].SyncEmailsForTest(t.Context(), clients[i], fakeMailboxes(), fake.config())
		}()
	}

	wg.Wait()

	for _, err := range errs {
		r.NoError(err)
	}

	r.Equal(int32(1), recorded.Load(),
		"one inbound email must mint exactly one result no matter how many consumers raced for it")
	r.Equal(fakeProcessedID, fake.mailboxOf("e1"))
}

// TestRejectedMoveRecordsNothingAndLeavesMessageForRetry covers the other half
// of the observed incident: when the archive fails, the old code had already
// recorded the result and left the message in the inbox to be recorded AGAIN
// on the next poll. Claiming first turns that into "record nothing, retry
// later".
func TestRejectedMoveRecordsNothingAndLeavesMessageForRetry(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	fake.rejectMoves.Store(true)
	seedInboxMessage(fake, "<reject@example.com>")

	var recorded atomic.Int32

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded})

	client := fake.client(t)
	mboxes := fakeMailboxes()

	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, fake.config()))
	r.Equal(int32(0), recorded.Load(), "a consumer that could not claim the message must not record it")
	r.Equal(fakeInboxID, fake.mailboxOf("e1"), "the message stays in the inbox for a later attempt")

	// The provider recovers; the retry is the first run that may record.
	fake.rejectMoves.Store(false)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, fake.config()))
	r.Equal(int32(1), recorded.Load())
	r.Equal(fakeProcessedID, fake.mailboxOf("e1"))
}

// TestClaimIsReleasedWhenTheHandlerFails proves a claimed-but-unprocessed
// message does not get stranded in Processed: the claim is handed back so the
// next sync retries it.
func TestClaimIsReleasedWhenTheHandlerFails(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	seedInboxMessage(fake, "<boom@example.com>")

	var recorded atomic.Int32

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded, fail: true})

	client := fake.client(t)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, fakeMailboxes(), fake.config()))

	r.Equal(int32(0), recorded.Load())
	r.Equal(fakeInboxID, fake.mailboxOf("e1"), "a failed handler must return the claim to the inbox")
}

// TestUnclaimedMailIsLeftInTheInbox guards the reason ClaimsEmail exists at
// all: claiming indiscriminately would drag unrelated mail out of the inbox
// and onto the wrong retention clock.
func TestUnclaimedMailIsLeftInTheInbox(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	fake.put("spam", &fakeEmail{
		mailbox:    fakeInboxID,
		to:         "", // recordingHandler.ClaimsEmail says no
		messageID:  "<spam@example.com>",
		receivedAt: time.Now().UTC(),
	})

	var recorded atomic.Int32

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded})

	client := fake.client(t)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, fakeMailboxes(), fake.config()))

	r.Equal(int32(0), recorded.Load())
	r.Equal(fakeInboxID, fake.mailboxOf("spam"))
	r.Equal(int32(0), fake.setCalls.Load(), "no Email/set should be issued for mail nobody claims")
}

// TestRescanRecoversStrandedClaim simulates the crash window: the message was
// moved to Processed but the result was never written. The re-scan must record
// it, and running the re-scan again must not record it a second time — here
// the handler's own Message-ID memo stands in for the database dedup that
// makes the replay safe in production.
func TestRescanRecoversStrandedClaim(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	fake.put("e1", &fakeEmail{
		mailbox:    fakeProcessedID,
		to:         "token@inbox.example.com",
		messageID:  "<stranded@example.com>",
		receivedAt: time.Now().UTC(),
	})

	var recorded atomic.Int32

	seen := &sync.Map{}

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded, seen: seen})

	client := fake.client(t)
	mboxes := fakeMailboxes()

	r.NoError(mgr.RescanProcessedForTest(t.Context(), client, mboxes))
	r.Equal(int32(1), recorded.Load(), "a stranded claim must be recovered")

	r.NoError(mgr.RescanProcessedForTest(t.Context(), client, mboxes))
	r.Equal(int32(1), recorded.Load(), "a second re-scan must not mint a duplicate")

	r.Equal(int32(0), fake.setCalls.Load(), "the re-scan must not move anything")
}

// TestRescanIgnoresMessagesOutsideTheWindow keeps the repair bounded: the
// re-scan exists to catch an in-flight claim, not to replay the archive.
func TestRescanIgnoresMessagesOutsideTheWindow(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fake := newFakeMailbox(t)
	fake.put("old", &fakeEmail{
		mailbox:    fakeProcessedID,
		to:         "token@inbox.example.com",
		messageID:  "<ancient@example.com>",
		receivedAt: time.Now().UTC().Add(-72 * time.Hour),
	})

	var recorded atomic.Int32

	mgr := jmap.NewManager(nil)
	mgr.RegisterHandler(&recordingHandler{recorded: &recorded})

	client := fake.client(t)
	r.NoError(mgr.RescanProcessedForTest(t.Context(), client, fakeMailboxes()))

	r.Equal(int32(0), recorded.Load(), "messages older than the re-scan window are not replayed")
}
