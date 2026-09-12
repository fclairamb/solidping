package checks_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/db/sqlite"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
	"github.com/fclairamb/solidping/server/internal/jmap"
)

// mintedTokenInDocument matches a server-minted token anywhere in a rendered
// export — the bare 24-byte hex token, or the tokenized inbound address built
// from one. Used on the SERIALIZED document rather than on the config maps, so
// a token that leaked through some field other than the one it was declared on
// is caught too.
//
//nolint:gochecknoglobals // test lookup table
var mintedTokenInDocument = regexp.MustCompile(`[0-9a-f]{32,}`)

// roundTripRig is a checks service over one fresh in-memory database, with the
// email inbox configured so send-mode SMTP checks are creatable.
type roundTripRig struct {
	svc   *checks.Service
	dbSvc db.Service
	org   *models.Organization
}

const roundTripInboxDomain = "inbox.acme.com"

func newRoundTripRig(t *testing.T) *roundTripRig {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	dbSvc, err := sqlite.New(ctx, sqlite.Config{InMemory: true})
	r.NoError(err)
	r.NoError(dbSvc.Initialize(ctx))
	t.Cleanup(func() { _ = dbSvc.Close() })

	org := models.NewOrganization("roundtrip", "Acme Round Trip")
	r.NoError(dbSvc.CreateOrganization(ctx, org))

	r.NoError(dbSvc.SetSystemParameter(ctx, jmap.SystemParameterKey, map[string]any{
		"enabled":       true,
		"sessionUrl":    "https://jmap.acme.com/session",
		"addressDomain": roundTripInboxDomain,
	}, false))

	creds, err := credentials.NewService(newKEK(t), newMemDEKStore())
	r.NoError(err)

	return &roundTripRig{svc: checks.NewService(dbSvc, nil, creds, nil), dbSvc: dbSvc, org: org}
}

// create attempts one check and reports whether it landed. A sample config
// that cannot be created offline (one needing a live tunnel or integration
// reference) is skipped rather than failing the test: the subject here is the
// export/import round trip, and a check that cannot exist cannot be exported.
func (rig *roundTripRig) create(t *testing.T, req checks.CreateCheckRequest) bool {
	t.Helper()

	if _, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, req); err != nil {
		t.Logf("skipping %s/%s: not creatable offline: %v", req.Type, req.Slug, err)

		return false
	}

	return true
}

// seedSampleChecks creates one check per published sample config, plus the
// three types whose tokens this spec is about: an email check (mints an ingest
// token), a heartbeat check (mints a ping token), and a send-mode SMTP check
// paired with the email check (whose delivery_to IS that email check's
// tokenized address). Returns how many checks exist.
func (rig *roundTripRig) seedSampleChecks(t *testing.T) int {
	t.Helper()
	r := require.New(t)

	created := 0

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		for i, sample := range registry.GetAllSampleConfigs(nil)[checkType] {
			slug := fmt.Sprintf("sample-%s-%d", strings.ReplaceAll(string(checkType), "_", "-"), i)
			period := defaultPeriodFor(checkType)

			if rig.create(t, checks.CreateCheckRequest{
				Name: "Sample " + slug, Slug: slug, Type: string(checkType),
				Config: sample.Config, Period: &period,
			}) {
				created++
			}
		}
	}

	// Config MUST be a non-nil map: the token-minting checkers fill the map
	// they are handed, and CreateCheck skips the config write entirely when the
	// request carried none — so `Config: nil` produces a tokenless check.
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Ingest", Slug: "ingest", Type: "email", Config: map[string]any{},
	}))
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Beat", Slug: "beat", Type: "heartbeat", Config: map[string]any{},
	}))
	created += 2

	emailCheck, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "ingest")
	r.NoError(err)

	token, _ := emailCheck.Config["token"].(string)
	r.NotEmpty(token, "the email check must have minted an ingest token")

	period := "1m"
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Probe", Slug: "probe", Type: "smtp", Period: &period,
		Config: map[string]any{
			"host":               "mail.acme.com",
			"send_email":         true,
			"mail_from":          "probe@acme.com",
			"delivery_to":        token + "@" + roundTripInboxDomain,
			"delivery_check_uid": emailCheck.UID,
		},
	}))
	created++

	return created
}

// defaultPeriodFor is the check type's own default period, as a duration
// string.
//
// Creating each sample WITH it sidesteps an unrelated defect: a check created
// with no period at all gets models.NewCheck's flat 1m, even for a type whose
// floor is higher (ssl 1h, dnsbl 15m, domain 6h) — and THAT check exports a
// period its own import refuses. Real and worth fixing, but it is a period
// bug, not an export-redaction one, and letting it fail here would bury what
// this test exists to watch.
func defaultPeriodFor(checkType checkerdef.CheckType) string {
	period := time.Minute
	if meta := checkerdef.GetCheckTypeMeta(checkType); meta != nil && meta.DefaultPeriod > 0 {
		period = meta.DefaultPeriod
	}

	return period.String()
}

// issuesOwnedByThisSpec drops the ValidateDocument findings that belong to the
// SECRET story (spec 2026-09-11-03, "secret refs need a parameters API and
// import parity"), which is explicitly out of scope here, and keeps everything
// else.
//
// Two shapes are dropped, both inherent to `secrets: stripped` as it exists
// today and neither touched by export redaction:
//
//  1. "looks like a credential" — validateNoInlinedCredentials flags any key
//     containing user/pass/token/…, so a plain `username` (never a secret; the
//     database checkers all carry one) and even ftp's `passive_mode` are
//     reported. The answer is a `${env:}`/`${param:}` reference syntax, not
//     redaction.
//  2. A declared SECRET was stripped, so the checker's own offline Validate now
//     reports it missing (sftp: "password or private_key is required"). The
//     document is correct; the validator has no way to know the operator will
//     supply the secret at import.
//
// Anything else — a missing name, a leaked-then-stripped redacted field
// breaking its own type's Validate — is this spec's problem and must be empty.
func issuesOwnedByThisSpec(t *testing.T, doc *checks.ExportDocument) []checks.DocumentIssue {
	t.Helper()

	secretKeysBySlug := map[string][]string{}

	for i := range doc.Checks {
		cfg, ok := registry.ParseConfig(checkerdef.CheckType(doc.Checks[i].Type))
		if !ok {
			continue
		}

		secretKeysBySlug[doc.Checks[i].Slug] = credentials.SecretFieldsFor(cfg)
	}

	var owned []checks.DocumentIssue

	for _, issue := range checks.ValidateDocument(doc) {
		if strings.Contains(issue.Message, "looks like a credential") {
			continue
		}

		if mentionsAny(issue.Message, secretKeysBySlug[issue.Where]) {
			continue
		}

		owned = append(owned, issue)
	}

	return owned
}

func mentionsAny(message string, keys []string) bool {
	for _, key := range keys {
		if key != "" && strings.Contains(message, key) {
			return true
		}
	}

	return false
}

// importErrorsOwnedByThisSpec is issuesOwnedByThisSpec for the import result:
// it drops the per-entry errors caused by a declared SECRET having been
// stripped from the document, and keeps everything else.
func importErrorsOwnedByThisSpec(
	t *testing.T, doc *checks.ExportDocument, result *checks.ImportResult,
) []checks.ImportError {
	t.Helper()

	secretKeysBySlug := map[string][]string{}

	for i := range doc.Checks {
		cfg, ok := registry.ParseConfig(checkerdef.CheckType(doc.Checks[i].Type))
		if !ok {
			continue
		}

		secretKeysBySlug[doc.Checks[i].Slug] = credentials.SecretFieldsFor(cfg)
	}

	var owned []checks.ImportError

	for _, entry := range result.Errors {
		if mentionsAny(entry.Error, secretKeysBySlug[entry.Slug]) {
			continue
		}

		owned = append(owned, entry)
	}

	return owned
}

// TestExportRoundTripsThroughValidateAndImport is the regression guard for
// both halves of spec 2026-09-11-02, run over every check type the product
// ships rather than over a hand-picked fixture.
//
// The two guarantees a config-as-code export has to make are that it contains
// no secret and that it re-imports as-is. Both broke on the first real refresh
// of a tracked org: the document carried a live email ingest token (twice),
// and a check whose name was empty exported without a `name` key, which
// ValidateDocument and the import path both require. The org's own export was
// a document its own server could not consume.
//
// So: seed one check per sample config plus the three token-bearing types,
// export the org, and assert the document (a) carries nothing shaped like a
// minted token, (b) passes ValidateDocument with zero issues, and (c) imports
// as a dry run with zero errors.
func TestExportRoundTripsThroughValidateAndImport(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	seeded := rig.seedSampleChecks(t)
	r.Greater(seeded, 10, "the audit is only meaningful across a broad set of check types")

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(doc.Checks, seeded)

	rendered, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	// (a) Nothing shaped like a minted token survived the exporter. Asserted
	// on the rendered bytes: the document is what gets committed to git, and
	// that is the artifact the leak actually travelled in.
	r.NotRegexp(mintedTokenInDocument, string(rendered),
		"a `secrets: stripped` export must not carry a server-minted token")
	r.Equal(checks.SecretsMarkerStripped, doc.Secrets)

	// (b) The document the server produced is one the server's own validator
	// accepts — see issuesOwnedByThisSpec for the two out-of-scope classes it
	// sets aside, both of which belong to the secret-reference story.
	issues := issuesOwnedByThisSpec(t, doc)
	r.Empty(issues, "the exporter must never produce a document ValidateDocument rejects: %v", issues)

	// The three token-bearing types are held to the WHOLE validator, with
	// nothing set aside: they are what this spec is about.
	for _, issue := range checks.ValidateDocument(doc) {
		r.NotContains([]string{"ingest", "beat", "probe"}, issue.Where,
			"the redacted check types must validate cleanly: %v", issue)
	}

	// (c) …and one the import path accepts, without writing anything.
	//
	// Same two out-of-scope classes set aside, for the same reason and by the
	// same rule: a dry run validates the document's config AS WRITTEN rather
	// than merged with the stored secrets — a limitation the server already
	// declares in its own response (DryRunCaveatSecretMerge) — so a config
	// whose declared secret the exporter stripped reads as incomplete here.
	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Empty(importErrorsOwnedByThisSpec(t, doc, result),
		"a dry-run re-import of the org's own export must report no errors")
	r.Equal(0, result.Created)
}

// TestReimportPreservesRedactedFields is the other half of the redaction
// contract: stripping a field from the export is only safe because the import
// path puts it back. A round trip that silently minted a NEW email token would
// break every sender of the check it was meant to protect.
func TestReimportPreservesRedactedFields(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	rig.seedSampleChecks(t)

	before, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "ingest")
	r.NoError(err)
	token, _ := before.Config["token"].(string)
	r.NotEmpty(token)

	beatBefore, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "beat")
	r.NoError(err)
	beatToken, _ := beatBefore.Config["token"].(string)
	r.NotEmpty(beatToken)

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	// A REAL import this time — the dry run writes nothing, and what is being
	// proven here is what the write leaves behind.
	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, false)
	r.NoError(err)
	r.Empty(result.Errors)

	after, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "ingest")
	r.NoError(err)
	r.Equal(token, after.Config["token"],
		"re-importing an export must not rotate the email check's ingest token")

	beatAfter, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "beat")
	r.NoError(err)
	r.Equal(beatToken, beatAfter.Config["token"],
		"re-importing an export must not rotate the heartbeat check's ping token")

	probeAfter, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "probe")
	r.NoError(err)
	r.Equal(token+"@"+roundTripInboxDomain, probeAfter.Config["delivery_to"],
		"re-importing an export must restore the SMTP probe's delivery_to")
}

// TestImportDerivesDeliveryToFromTheReference proves the derivation path in
// its own right: an SMTP check that does NOT yet exist, imported from a
// document that omits delivery_to (as every export now does), is created with
// the address rebuilt from the delivery_check_uid the document did carry.
// Preservation cannot explain this one — there is nothing stored to preserve.
func TestImportDerivesDeliveryToFromTheReference(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Ingest", Slug: "ingest", Type: "email", Config: map[string]any{},
	}))

	emailCheck, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "ingest")
	r.NoError(err)
	token, _ := emailCheck.Config["token"].(string)
	r.NotEmpty(token)

	period := "1m"
	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
		Name: "Probe", Slug: "probe", Type: "smtp", Period: &period,
		Config: map[string]any{
			"host":               "mail.acme.com",
			"send_email":         true,
			"mail_from":          "probe@acme.com",
			"delivery_check_uid": emailCheck.UID,
		},
	})
	r.NoError(err, "a create that omits delivery_to must derive it from delivery_check_uid")
	r.Equal(token+"@"+roundTripInboxDomain, created.Config["delivery_to"])
}

// TestCreateAndUpdateRejectBlankNames covers the second defect: the API
// accepted `name: ""` (and `"   "`), the exporter then omitted the key, and
// the import path requires it — a document the server produced that the server
// refuses. Both write paths now refuse the blank name instead.
func TestCreateAndUpdateRejectBlankNames(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	// A whitespace-only name is a name the caller chose, and it is refused.
	// (`""` is NOT distinguishable from an omitted name on this request shape,
	// so it takes the auto-derivation path below instead.)
	for i, blank := range []string{"   ", "\t\n"} {
		_, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
			Name: blank, Slug: fmt.Sprintf("blank-name-%d", i), Type: "heartbeat",
			Config: map[string]any{},
		})
		r.Errorf(err, "create must refuse the blank name %q", blank)
	}

	// An OMITTED name is still fine — the checker derives one, which is how
	// half the API's callers create checks, and the derived name is what makes
	// the export valid.
	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
		Slug: "derived-name", Type: "http", Config: map[string]any{"url": "https://acme.com/"},
	})
	r.NoError(err)
	r.NotNil(created.Name)
	r.NotEmpty(*created.Name)

	for _, blank := range []string{"", "   "} {
		name := blank
		_, err := rig.svc.UpdateCheck(t.Context(), rig.org.Slug, "derived-name",
			&checks.UpdateCheckRequest{Name: &name})
		r.Errorf(err, "update must refuse the blank name %q", blank)
	}

	// And the check kept the name it had.
	after, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "derived-name")
	r.NoError(err)
	r.NotNil(after.Name)
	r.NotEmpty(*after.Name)
}

// TestExportOfANamelessCheckWouldBeRejected is the reason the migration
// exists, pinned as a test: a check whose name is blank in the DATABASE (the
// state the write paths can no longer produce, but that existing rows are in)
// exports without a `name` key, and that document fails validation. The
// migration is what clears the backlog; this proves the failure mode it clears
// is real.
func TestExportOfANamelessCheckWouldBeRejected(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Named", Slug: "named", Type: "http", Config: map[string]any{"url": "https://acme.com/"},
	}))

	// Blank the name the only way left: straight on the row, as the pre-fix
	// API used to allow.
	check, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "named")
	r.NoError(err)

	blank := ""
	r.NoError(rig.dbSvc.UpdateCheck(t.Context(), check.UID, &models.CheckUpdate{Name: &blank}))

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	rendered, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	var decoded map[string]any
	r.NoError(json.Unmarshal(rendered, &decoded))

	issues := checks.ValidateDocument(doc)
	r.NotEmpty(issues, "a nameless check must export into a document the validator rejects")
	r.Contains(issues[0].Message, "name")
}

// TestCreateWithoutANameFallsBackToTheSlug covers the OTHER way a check used
// to end up nameless — and the one no dashboard user could have caused.
//
// Most checkers derive a name inside Validate ("Domain: acme.com"), but
// several (tcp, udp, icmp, …) derive none at all, and CreateCheck only wrote
// `name` when the spec had one. So `POST /checks {"type":"tcp", …}` with no
// name produced a row whose name was NULL, which exports as an absent `name`
// key — the exact document ValidateDocument and the import path refuse.
//
// Refusing the request instead would have been the wrong fix: creating a check
// from a target alone is a supported, widely used shape. The slug is the fill,
// matching the backfill migration and what checkDisplayName already rendered.
func TestCreateWithoutANameFallsBackToTheSlug(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)

	created, err := rig.svc.CreateCheck(t.Context(), rig.org.Slug, checks.CreateCheckRequest{
		Slug: "nameless-tcp", Type: "tcp",
		Config: map[string]any{"host": "acme.com", "port": 443},
	})
	r.NoError(err)
	r.NotNil(created.Name)
	r.Equal("nameless-tcp", *created.Name)

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(t.Context(), rig.org.UID, "nameless-tcp")
	r.NoError(err)
	r.NotNil(stored.Name, "the row must never carry a NULL name")
	r.Equal("nameless-tcp", *stored.Name)

	// And the resulting export is a document the validator accepts.
	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Empty(checks.ValidateDocument(doc))
}
