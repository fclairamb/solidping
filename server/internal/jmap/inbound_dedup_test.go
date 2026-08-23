package jmap_test

import (
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/emailcheck"
	"github.com/fclairamb/solidping/server/internal/jmap"
	"github.com/fclairamb/solidping/server/internal/jobs/jobsvc"
	"github.com/fclairamb/solidping/server/internal/notifier"
)

// inboundToken is a 48-hex email-check token, the shape the recipient's local
// part must have for emailcheck to claim the message.
const inboundToken = "abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef"

// inboundFixture wires the real pieces end to end: a SQLite database, a real
// emailcheck handler, and the fake mailbox. Nothing here is a stub except the
// mail server, which is exactly the boundary the feature is about.
type inboundFixture struct {
	svc   *sqlite.Service
	check *models.Check
	fake  *fakeMailbox
}

func newInboundFixture(t *testing.T) *inboundFixture {
	t.Helper()

	r := require.New(t)
	ctx := t.Context()

	svc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(svc.Initialize(ctx))
	t.Cleanup(func() { _ = svc.Close() })

	org := models.NewOrganization("inbound-org", "Inbound Org")
	r.NoError(svc.CreateOrganization(ctx, org))

	check := models.NewCheck(org.UID, "inbound-check", "email")
	check.Config = models.JSONMap{"token": inboundToken}
	r.NoError(svc.CreateCheck(ctx, check))

	return &inboundFixture{svc: svc, check: check, fake: newFakeMailbox(t)}
}

// newManager builds one "replica": its own manager and client over the shared
// database and the shared mailbox.
func (f *inboundFixture) newManager(t *testing.T) (*jmap.Manager, *jmap.Client) {
	t.Helper()

	jobSvc := jobsvc.NewService(f.svc.DB(), f.svc, notifier.NewLocalEventNotifier(), nil)
	handler := emailcheck.NewHandler(f.svc, jobSvc, nil, nil, slog.Default())

	mgr := jmap.NewManager(f.svc)
	mgr.RegisterHandler(handler)

	return mgr, f.fake.client(t)
}

// resultsFor counts raw results carrying the given Message-ID — the number the
// acceptance criterion is about.
func resultsFor(t *testing.T, svc db.Service, checkUID, messageID string) int {
	t.Helper()

	count, err := svc.DB().NewSelect().
		Model((*models.Result)(nil)).
		Where("check_uid = ?", checkUID).
		Where("period_type = ?", models.PeriodTypeRaw).
		Where("json_extract(output, '$.messageId') = ?", messageID).
		Count(t.Context())
	require.NoError(t, err)

	return count
}

func (f *inboundFixture) deliver(id, messageID string) {
	f.fake.put(id, &fakeEmail{
		mailbox:    fakeInboxID,
		to:         inboundToken + "@inbox.example.com",
		messageID:  messageID,
		subject:    "delivery probe",
		receivedAt: time.Now().UTC(),
	})
}

// TestInboundEmailYieldsExactlyOneResultAcrossReplicas is the acceptance test
// for spec 2026-08-22-01, over the real handler and a real database: two
// replicas contend for one message and the results table ends up with exactly
// one row for it.
func TestInboundEmailYieldsExactlyOneResultAcrossReplicas(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fix := newInboundFixture(t)
	fix.fake.setBarrier = newBarrier(2)
	fix.deliver("e1", "<replicas@mail.example.com>")

	mgrA, clientA := fix.newManager(t)
	mgrB, clientB := fix.newManager(t)

	mboxes := fakeMailboxes()
	cfg := fix.fake.config()

	var wg sync.WaitGroup

	wg.Add(2)

	var errA, errB error

	go func() {
		defer wg.Done()

		errA = mgrA.SyncEmailsForTest(t.Context(), clientA, mboxes, cfg)
	}()

	go func() {
		defer wg.Done()

		errB = mgrB.SyncEmailsForTest(t.Context(), clientB, mboxes, cfg)
	}()

	wg.Wait()
	r.NoError(errA)
	r.NoError(errB)

	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, "<replicas@mail.example.com>"),
		"one inbound email must produce exactly one raw result")
}

// TestSingleReplicaRecordsTheEmail is the positive control: it proves the
// fixture really delivers a recordable email, so "exactly one" above cannot be
// satisfied by a pipeline that silently records nothing.
func TestSingleReplicaRecordsTheEmail(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fix := newInboundFixture(t)
	fix.deliver("e1", "<solo@mail.example.com>")

	mgr, client := fix.newManager(t)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, fakeMailboxes(), fix.fake.config()))

	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, "<solo@mail.example.com>"))
}

// TestReprocessedMessageDoesNotDuplicate covers the move-failure path all the
// way down: the archive is refused, so nothing is recorded and the message is
// retried; once it succeeds it is recorded once — and a further replay (the
// case where a message somehow reaches the handler twice) is absorbed by the
// Message-ID backstop rather than adding a row.
func TestReprocessedMessageDoesNotDuplicate(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const messageID = "<retry@mail.example.com>"

	fix := newInboundFixture(t)
	fix.fake.rejectMoves.Store(true)
	fix.deliver("e1", messageID)

	mgr, client := fix.newManager(t)
	mboxes := fakeMailboxes()
	cfg := fix.fake.config()

	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, cfg))
	r.Equal(0, resultsFor(t, fix.svc, fix.check.UID, messageID),
		"a consumer that could not claim the message must not record it")
	r.Equal(fakeInboxID, fix.fake.mailboxOf("e1"))

	fix.fake.rejectMoves.Store(false)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, cfg))
	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, messageID))

	// Force a replay of the very same message: put it back in the inbox and
	// sync again. Only the dedup backstop stands between this and a second row.
	fix.deliver("e1", messageID)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, cfg))
	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, messageID),
		"a replayed message must not mint a second result")
}

// TestRescanRecordsStrandedMessageOnceEndToEnd is the crash-recovery case over
// the real database: a message sits in Processed with no result (its claimer
// died mid-flight); the re-scan records it, and running the re-scan again
// leaves the count at one.
func TestRescanRecordsStrandedMessageOnceEndToEnd(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	const messageID = "<stranded@mail.example.com>"

	fix := newInboundFixture(t)
	fix.fake.put("e1", &fakeEmail{
		mailbox:    fakeProcessedID,
		to:         inboundToken + "@inbox.example.com",
		messageID:  messageID,
		subject:    "stranded",
		receivedAt: time.Now().UTC(),
	})

	mgr, client := fix.newManager(t)
	mboxes := fakeMailboxes()

	r.NoError(mgr.RescanProcessedForTest(t.Context(), client, mboxes))
	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, messageID),
		"a message stranded in Processed must be recovered")

	r.NoError(mgr.RescanProcessedForTest(t.Context(), client, mboxes))
	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, messageID),
		"a second re-scan must not mint a duplicate")
}

// TestDistinctMessagesEachRecord is the dedup layer's negative control: the
// backstop must suppress duplicates, never distinct deliveries. Without it, a
// dedup bug that swallowed everything would look identical to a working one.
func TestDistinctMessagesEachRecord(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fix := newInboundFixture(t)
	fix.deliver("e1", "<first@mail.example.com>")

	mgr, client := fix.newManager(t)
	mboxes := fakeMailboxes()
	cfg := fix.fake.config()

	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, cfg))

	fix.deliver("e2", "<second@mail.example.com>")
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, mboxes, cfg))

	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, "<first@mail.example.com>"))
	r.Equal(1, resultsFor(t, fix.svc, fix.check.UID, "<second@mail.example.com>"))
}

// TestUnknownTokenIsClaimedAndRejected pins the one place where claiming is
// deliberately broader than recording: a token-shaped address that resolves to
// no check is archived (so it stops being re-read forever) without producing a
// result.
func TestUnknownTokenIsClaimedAndRejected(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	fix := newInboundFixture(t)
	fix.fake.put("e1", &fakeEmail{
		mailbox:    fakeInboxID,
		to:         "0000000000000000000000000000000000000000deadbeef@inbox.example.com",
		messageID:  "<unknown@mail.example.com>",
		receivedAt: time.Now().UTC(),
	})

	mgr, client := fix.newManager(t)
	r.NoError(mgr.SyncEmailsForTest(t.Context(), client, fakeMailboxes(), fix.fake.config()))

	r.Equal(fakeProcessedID, fix.fake.mailboxOf("e1"),
		"an unresolvable token must be filed away, not left to be re-read forever")
	r.Equal(0, resultsFor(t, fix.svc, fix.check.UID, "<unknown@mail.example.com>"))
}
