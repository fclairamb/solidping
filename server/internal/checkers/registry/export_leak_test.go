package registry_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/registry"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/handlers/checks"
)

// mintedTokenShapes are the two shapes a SolidPing-minted check token takes in
// a config value: the bare token (heartbeat's ping token, email's ingest
// token — 24 random bytes, so 48 lowercase hex chars) and the tokenized
// inbound address built from one (`<token>@<domain>`, which is what a
// send-mode SMTP check's delivery_to holds).
//
// 32 is the floor rather than 48 so a future checker minting a 16-byte token
// is covered too. Both are anchored at the start: a value that merely CONTAINS
// a long hex run (a checksum in a URL path, say) is not a token and must not
// be flagged — this tripwire has to stay quiet enough that nobody is tempted
// to weaken it.
//
//nolint:gochecknoglobals // test lookup table
var mintedTokenShapes = []*regexp.Regexp{
	regexp.MustCompile(`^[0-9a-f]{32,}$`),
	regexp.MustCompile(`^[0-9a-f]{32,}@`),
}

func looksLikeMintedToken(value string) bool {
	for _, re := range mintedTokenShapes {
		if re.MatchString(value) {
			return true
		}
	}

	return false
}

// exportAuditSpecs returns the configs to run through the exporter for one
// check type: every sample the checker publishes, plus an EMPTY config.
//
// The empty config is the one that matters most here. Neither email nor
// heartbeat publishes samples, and both mint their token inside Validate() —
// so handing the checker an empty config and letting it fill itself in is the
// only way to get a realistic, freshly-minted token for those two types
// without hard-coding one the audit could drift away from.
func exportAuditSpecs(
	checkType checkerdef.CheckType, samples map[checkerdef.CheckType][]checkerdef.CheckSpec,
) []checkerdef.CheckSpec {
	specs := []checkerdef.CheckSpec{{Config: map[string]any{}}}
	specs = append(specs, samples[checkType]...)

	// checksmtp publishes no samples and its delivery_to is only ever filled
	// by an operator pairing the check with an email check, so the leaked
	// shape is spelled out: the paired inbox's tokenized address, exactly as
	// the real exp-devops export carried it.
	if checkType == checkerdef.CheckTypeSMTP {
		specs = append(specs, checkerdef.CheckSpec{Config: map[string]any{
			"host":               "mail.acme.com",
			"send_email":         true,
			"mail_from":          "probe@acme.com",
			"delivery_to":        "6f1c0f4a2b8e4d1390ab57c26d4e83f10b7a9c5d2e6f4813@inbox.acme.com",
			"delivery_check_uid": "chk_email_1",
		}})
	}

	return specs
}

// realizeConfig runs the checker's own Validate over a COPY of the spec's
// config, so token-minting checkers fill it in exactly as a real create would,
// and returns the resulting config. Validate's error is ignored on purpose: a
// sample that cannot pass offline validation (one needing a live tunnel
// reference, say) still has a config worth auditing, and a checker that
// refuses an empty config simply mints nothing.
func realizeConfig(checker checkerdef.Checker, spec checkerdef.CheckSpec) map[string]any {
	config := map[string]any{}
	for k, v := range spec.Config {
		config[k] = v
	}

	local := checkerdef.CheckSpec{Name: spec.Name, Slug: spec.Slug, Config: config}
	_ = checker.Validate(&local)

	return local.Config
}

// collectStringValues walks a config and returns every string it contains,
// nested maps and slices included — a token hidden one level down is a leak
// like any other.
func collectStringValues(value any, out *[]string) {
	switch typed := value.(type) {
	case string:
		*out = append(*out, typed)
	case map[string]any:
		for _, v := range typed {
			collectStringValues(v, out)
		}
	case []any:
		for _, v := range typed {
			collectStringValues(v, out)
		}
	case json.Number:
	default:
	}
}

func configStrings(config map[string]any) []string {
	var out []string
	collectStringValues(config, &out)

	return out
}

// TestExportNeverCarriesAMintedToken is the sibling of
// TestNoUndeclaredCheckerSecrets, for the question that test does not ask
// (spec 2026-09-11-02).
//
// TestNoUndeclaredCheckerSecrets asks "is every credential-looking field
// declared secret?", and it is deliberately satisfied by the email and
// heartbeat tokens being allowlisted as public-by-design — they must stay in
// the queryable public column or inbound matching and the ping URL break. That
// is a correct STORAGE answer that was silently taken for an EXPORT answer:
// the exporter stripped only SecretFields(), so a document stamped
// `secrets: stripped` carried a live 48-hex-char ingest token, twice, into a
// customer's git history.
//
// So this one asks the export question directly, end to end: for every
// registered checker, realize its config the way a create would (Validate
// mints what it mints), run it through the REAL exporter, and assert nothing
// that comes out has the shape of a minted token. A future checker that adds a
// server-minted public identifier fails here rather than in somebody's repo.
func TestExportNeverCarriesAMintedToken(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	samples := registry.GetAllSampleConfigs(nil)

	for _, checkType := range checkerdef.ListCheckTypes(nil) {
		checker, ok := registry.GetChecker(checkType)
		r.Truef(ok, "GetChecker must know registered type %q", checkType)

		for _, spec := range exportAuditSpecs(checkType, samples) {
			stored := realizeConfig(checker, spec)
			exported := checks.ExportedConfigFor(&models.Check{Type: string(checkType), Config: stored})

			for _, value := range configStrings(exported) {
				r.Falsef(looksLikeMintedToken(value),
					"checker %q exports %q, which has the shape of a server-minted token — "+
						"declare the key in the config's ExportRedactedFields() so the exporter strips it",
					checkType, value)
			}
		}
	}
}

// TestExportLeakTripwireIsNotVacuous is the positive control for the test
// above: it proves the three configs that ACTUALLY carry a minted token carry
// one before the exporter runs, so a green tripwire means "the exporter
// stripped it", never "there was nothing to strip".
//
// Without this, deleting the ExportRedactedFields() declarations and breaking
// token minting at the same time would leave TestExportNeverCarriesAMintedToken
// passing.
func TestExportLeakTripwireIsNotVacuous(t *testing.T) {
	t.Parallel()

	r := require.New(t)
	samples := registry.GetAllSampleConfigs(nil)

	leaky := map[checkerdef.CheckType]string{
		checkerdef.CheckTypeEmail:     "token",
		checkerdef.CheckTypeHeartbeat: "token",
		checkerdef.CheckTypeSMTP:      "delivery_to",
	}

	for checkType, field := range leaky {
		checker, ok := registry.GetChecker(checkType)
		r.Truef(ok, "GetChecker must know %q", checkType)

		found := false

		for _, spec := range exportAuditSpecs(checkType, samples) {
			stored := realizeConfig(checker, spec)

			value, _ := stored[field].(string)
			if looksLikeMintedToken(value) {
				found = true

				// And the exporter is what removes it — not an accident of the
				// value's shape.
				exported := checks.ExportedConfigFor(&models.Check{Type: string(checkType), Config: stored})
				_, stillThere := exported[field]
				r.Falsef(stillThere, "%q config key %q must not survive the exporter", checkType, field)
			}
		}

		r.Truef(found,
			"the audit must actually produce a minted-token-shaped %q.%s, or it proves nothing",
			checkType, field)
	}
}
