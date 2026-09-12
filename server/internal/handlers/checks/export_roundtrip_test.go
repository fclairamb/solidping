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
	"github.com/fclairamb/solidping/server/internal/regions"
)

// mintedTokenInDocument matches a server-minted token anywhere in a rendered
// export — the bare 24-byte hex token, or the tokenized inbound address built
// from one. Used on the SERIALIZED document rather than on the config maps, so
// a token that leaked through some field other than the one it was declared on
// is caught too.
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

// issuesOwnedByThisSpec used to drop two classes of ValidateDocument finding
// that spec 2026-09-11-02 left open and declared out of its scope. Spec
// 2026-09-11-04 CLOSED both, so it now filters nothing and exists only as the
// name the assertions below read by:
//
//  1. "looks like a credential" fired on any key containing user/pass/token/…,
//     so a plain `username` (never a secret — every database checker carries
//     one) and even ftp's `passive_mode` were reported. The hint is now
//     anchored on what the checker DECLARES secret, which registry's
//     TestNoUndeclaredCheckerSecrets independently guarantees is the complete
//     set of credential fields.
//  2. A declared secret the exporter stripped made the checker's own offline
//     Validate report the config incomplete (sftp: "password or private_key is
//     required"). On a `secrets: stripped` document that complaint is now
//     suppressed by PARAMETER — the import merge is what puts the value back.
//
// Keeping it as a pass-through, rather than deleting it, is deliberate: if a
// future change reopens either class, the failure lands on the guarantee
// itself instead of on a filter somebody has to notice was doing work.
func issuesOwnedByThisSpec(t *testing.T, doc *checks.ExportDocument) []checks.DocumentIssue {
	t.Helper()

	return checks.ValidateDocument(doc)
}

// importErrorsOwnedByThisSpec is the same pass-through for the import result:
// a dry run now injects a PLACEHOLDER for every key the row advertises as
// private, reproducing the shape of the merge it must not perform, so a
// stripped declared secret no longer fails the plan either.
func importErrorsOwnedByThisSpec(
	t *testing.T, _ *checks.ExportDocument, result *checks.ImportResult,
) []checks.ImportError {
	t.Helper()

	return result.Errors
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
	// that is the artifact the leak actually traveled in.
	r.NotRegexp(mintedTokenInDocument, string(rendered),
		"a `secrets: stripped` export must not carry a server-minted token")
	r.Equal(checks.SecretsMarkerStripped, doc.Secrets)

	// (b) The document the server produced is one the server's own validator
	// accepts — with nothing set aside since spec 2026-09-11-04 (see
	// issuesOwnedByThisSpec).
	issues := issuesOwnedByThisSpec(t, doc)
	r.Empty(issues, "the exporter must never produce a document ValidateDocument rejects: %v", issues)

	// The three token-bearing types are held to the WHOLE validator, with
	// nothing set aside: they are what this spec is about.
	for _, issue := range checks.ValidateDocument(doc) {
		r.NotContains([]string{"ingest", "beat", "probe"}, issue.Where,
			"the redacted check types must validate cleanly: %v", issue)
	}

	// (c) …and one the import path accepts, without writing anything.
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
		_, updateErr := rig.svc.UpdateCheck(t.Context(), rig.org.Slug, "derived-name",
			&checks.UpdateCheckRequest{Name: &name})
		r.Errorf(updateErr, "update must refuse the blank name %q", blank)
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

// seedRoundTripFixture adds, on top of the per-sample checks, the organization
// shape the round-trip guarantee has to survive and that a bare sample sweep
// does not exercise: a group, labels, a dependency edge, an org default region
// set, and the two spellings of a private location — the folded `@paris` the
// server stores and exports, and the long `@roundtrip/paris` its own
// documentation says is accepted on input.
//
// The long spelling is the single most important case here. On the tracked org
// that motivated this spec, 183 of 197 "errors" were exactly that: the file
// said `@stonaltech/aws-paris`, the export said `@aws-paris`, and every
// external validator concluded the file was wrong.
func (rig *roundTripRig) seedRoundTripFixture(t *testing.T) int {
	t.Helper()
	r := require.New(t)
	ctx := t.Context()

	r.NoError(rig.dbSvc.SetOrgParameter(ctx, rig.org.UID, regions.ParamDefaultRegions,
		[]string{"default"}, false))

	group := models.NewCheckGroup(rig.org.UID, "Edge", "edge")
	r.NoError(rig.dbSvc.CreateCheckGroup(ctx, group))

	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Parent", Slug: "fixture-parent", Type: "http",
		Config: map[string]any{"url": "https://acme.com/parent"},
		Labels: map[string]string{"tier": "edge", "owner": "platform"},
	}))

	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Child", Slug: "fixture-child", Type: "http",
		Config:        map[string]any{"url": "https://acme.com/child"},
		CheckGroupUID: &group.UID,
		Labels:        map[string]string{"tier": "edge"},
	}))

	// The long private-location spelling on the way IN. What comes back out is
	// the folded one, and the assertion below is that the difference is not a
	// difference.
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Private", Slug: "fixture-private", Type: "http",
		Config:  map[string]any{"url": "https://acme.com/private"},
		Regions: []string{"@" + rig.org.Slug + "/paris"},
	}))

	parent, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "fixture-parent")
	r.NoError(err)
	child, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "fixture-child")
	r.NoError(err)

	r.NoError(rig.dbSvc.CreateCheckDependency(ctx,
		models.NewCheckDependency(rig.org.UID, child.UID, parent.UID, models.CheckDependencyKindHard, nil)))

	stored, err := rig.dbSvc.GetCheckByUidOrSlug(ctx, rig.org.UID, "fixture-private")
	r.NoError(err)
	r.Equal([]string{"@paris"}, stored.Regions,
		"the long @org/location spelling must be stored folded — that is what the export then carries")

	return 3
}

// roundTripPlan is the three answers the guarantee is made of, for one
// document: the validator's issues, the import dry run and the apply dry run.
type roundTripPlan struct {
	issues    []checks.DocumentIssue
	imported  *checks.ImportResult
	applied   *checks.ApplyResult
	checkList int
}

// planRoundTrip exports the org and runs the three questions against the
// result. Deliberately NOT a helper with assertions inside: each caller
// asserts, so a failure names the document it came from.
func (rig *roundTripRig) planRoundTrip(t *testing.T) (*checks.ExportDocument, roundTripPlan) {
	t.Helper()
	r := require.New(t)

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)

	imported, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)

	applied, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, doc, checks.ApplyOptions{DryRun: true})
	r.NoError(err)

	return doc, roundTripPlan{
		issues:    issuesOwnedByThisSpec(t, doc),
		imported:  imported,
		applied:   applied,
		checkList: len(doc.Checks),
	}
}

// assertRoundTrip is the contract, stated once:
//
//	export → validate-document = 0 issues
//	export → import(dryRun)    = 0 create / 0 update / N unchanged
//	export → apply(dryRun)     = same, 0 unmanaged
//
// The import dry run is allowed the ONE class of per-entry error spec
// 2026-09-11-02 documented and this spec does not close: a check whose declared
// secret the exporter stripped fails the checker's offline Validate, because a
// dry run validates the document's config as written rather than merged with
// the stored secret (DryRunCaveatSecretMerge, which the server declares in its
// own response). Those entries produce no plan row at all, so the unchanged
// count is measured against the entries that did plan.
func assertRoundTrip(t *testing.T, doc *checks.ExportDocument, plan roundTripPlan, requireManaged bool) {
	t.Helper()
	r := require.New(t)

	r.Empty(plan.issues, "the server's own export must validate cleanly: %v", plan.issues)

	r.Empty(importErrorsOwnedByThisSpec(t, doc, plan.imported),
		"a dry-run re-import of the org's own export must report no errors")
	r.Equal(0, plan.imported.Created, "a fresh export creates nothing")
	r.Equal(0, plan.imported.Updated,
		"a fresh export changes nothing — every reported update is drift the round trip invented: %v",
		updatedSlugs(plan.imported))
	r.Equal(plan.checkList-len(plan.imported.Errors), plan.imported.Unchanged,
		"every entry that planned at all must plan as unchanged")

	// Anti-vacuity floor. "0 created, 0 updated" is also what a document that
	// failed on every single entry answers, so the unchanged count has to be
	// asserted as a POPULATION, not only as the complement of the errors.
	r.Greater(plan.imported.Unchanged, 10,
		"only %d of %d entries planned at all — the guarantee above is vacuous",
		plan.imported.Unchanged, plan.checkList)

	r.Equal(0, plan.applied.Created)
	r.Equal(0, plan.applied.Updated, "apply dry run reports drift a fresh export cannot have: %v",
		changedApplySlugs(plan.applied))
	r.Equal(0, plan.applied.Deleted)

	if requireManaged {
		r.Equal(0, plan.applied.Unmanaged, "every check is owned by the manifest after a real apply")
		r.Equal(plan.checkList, plan.applied.Unchanged)
	}
}

// updatedSlugs / changedApplySlugs render the offending entries WITH their
// field diffs, so a failure says which field moved rather than only that the
// count was wrong. Being able to read that off the failure is the whole reason
// the plan carries a field diff at all.
func updatedSlugs(result *checks.ImportResult) []checks.ImportPlanEntry {
	var out []checks.ImportPlanEntry
	for _, entry := range result.Plan {
		if entry.Action == checks.ActionUpdate {
			out = append(out, entry)
		}
	}

	return out
}

func changedApplySlugs(result *checks.ApplyResult) []checks.ApplyPlanEntry {
	var out []checks.ApplyPlanEntry
	for _, entry := range result.Plan {
		if entry.Action == checks.ApplyActionUpdate {
			out = append(out, entry)
		}
	}

	return out
}

// TestExportRoundTripsAsUnchanged is the guarantee spec 2026-09-11-04 exists to
// make, over every check type the product ships plus a fixture org carrying
// defaults, a private location, a group, labels and a dependency.
//
// The failure it pins is not hypothetical: `import --dry-run` on a file that
// was byte-for-byte the current export answered `created=1 updated=482`, so the
// one question config-as-code exists to answer — does this file match the
// instance? — had no answer at all short of a client-side diff, and every
// external tool that grew one drifted from the server immediately.
func TestExportRoundTripsAsUnchanged(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	seeded := rig.seedSampleChecks(t) + rig.seedRoundTripFixture(t)
	r.Greater(seeded, 10, "the guarantee is only meaningful across a broad set of check types")

	// (1) A fresh export, against an org nothing has applied yet.
	doc, plan := rig.planRoundTrip(t)
	r.Len(doc.Checks, seeded)
	assertRoundTrip(t, doc, plan, false)

	// The folded region really did survive, and is what the document carries.
	for i := range doc.Checks {
		if doc.Checks[i].Slug == "fixture-private" {
			r.Equal([]string{"@paris"}, doc.Checks[i].Regions,
				"the folded @location form is the canonical export spelling")
		}
	}

	// (2) A REAL apply, which is what stamps the managed label — then the same
	// three questions again. This is the state an operator's repository is
	// actually in, and `unmanaged` must be zero in it.
	applied, err := rig.svc.ApplyChecks(t.Context(), rig.org.Slug, doc, checks.ApplyOptions{})
	r.NoError(err)
	r.Empty(importErrorsOwnedByThisSpec(t, doc, &checks.ImportResult{Errors: applied.Errors}),
		"a real apply of the org's own export must not fail: %v", applied.Errors)

	managedDoc, managedPlan := rig.planRoundTrip(t)
	assertRoundTrip(t, managedDoc, managedPlan, true)

	// (3) export → export idempotence: the second document is byte-identical
	// to the first apart from its timestamp, and answers the same three ways.
	againDoc, againPlan := rig.planRoundTrip(t)
	assertRoundTrip(t, againDoc, againPlan, true)
	r.Equal(renderWithoutTimestamp(t, managedDoc), renderWithoutTimestamp(t, againDoc),
		"exporting twice in a row must produce the same document")
}

// renderWithoutTimestamp renders a document for comparison with exportedAt
// removed — it always differs and never means drift, which is exactly the
// normalization `sp checks diff` performs.
func renderWithoutTimestamp(t *testing.T, doc *checks.ExportDocument) string {
	t.Helper()
	r := require.New(t)

	rendered, err := checks.MarshalExportDocument(doc)
	r.NoError(err)

	var generic map[string]any
	r.NoError(json.Unmarshal(rendered, &generic))
	delete(generic, "exportedAt")

	normalized, err := json.Marshal(generic)
	r.NoError(err)

	return string(normalized)
}

// TestLongPrivateRegionSpellingIsNotADifference is the 183-error case on its
// own, stated as small as it can be: a manifest written with the documented
// long `@org/location` spelling must plan as unchanged against a check stored
// with the folded one. Nothing else in this file isolates it — in the sweep
// above it is one check among fifty.
func TestLongPrivateRegionSpellingIsNotADifference(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	rig := newRoundTripRig(t)
	r.True(rig.create(t, checks.CreateCheckRequest{
		Name: "Private", Slug: "private", Type: "http",
		Config:  map[string]any{"url": "https://acme.com/private"},
		Regions: []string{"@paris"},
	}))

	doc, err := rig.svc.ExportChecks(t.Context(), rig.org.Slug, checks.ListChecksOptions{})
	r.NoError(err)
	r.Len(doc.Checks, 1)

	// Rewrite the document the way a human following the documentation would.
	doc.Checks[0].Regions = []string{"@" + rig.org.Slug + "/paris"}

	result, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Empty(result.Errors, "%+v", result.Errors)
	r.Equal(0, result.Updated, "the long spelling is the same region, not a change: %v", result.Plan)
	r.Equal(1, result.Unchanged)

	// Negative control: a region that really IS different must still report.
	doc.Checks[0].Regions = []string{"default"}
	changed, err := rig.svc.ImportChecks(t.Context(), rig.org.Slug, doc, true)
	r.NoError(err)
	r.Equal(1, changed.Updated, "a real region change must not be swallowed by the folding")
	r.Len(changed.Plan, 1)
	r.Equal([]checks.CheckFieldChange{{Field: "regions", From: "@paris", To: "default"}},
		changed.Plan[0].Changes)
}
