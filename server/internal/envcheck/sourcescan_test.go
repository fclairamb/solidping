package envcheck

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// spNameLiteral matches a whole (quoted, then unquoted) string literal that is
// exactly an SP_* name: multiple uppercase/digit segments separated by
// underscores. Whole-literal matching (not substring search) is what lets this
// pick up both `os.Getenv("SP_...")` call sites and standalone constants such
// as envEntitlementsUpgradeTokenSecret, while naturally skipping log/error
// messages that merely mention a variable in prose (e.g. "set
// SP_ENCRYPTION_MASTER_KEY first") and the bare "SP_" koanf prefix itself.
var spNameLiteral = regexp.MustCompile(`^SP_[A-Z0-9_]+$`)

// sourceScanExcludedDirs are directories under the server module root that are
// not server env readers, so a bare SP_* string literal found there proves
// nothing about what the running server recognizes.
var sourceScanExcludedDirs = map[string]string{
	// This package's own allowlists are exactly what the scan checks against;
	// scanning them is circular (every literal here is definitionally
	// recognized) and adds nothing.
	"internal/envcheck": "the allowlists under test",
	// membench boots child server processes and sets their env from a map
	// literal — it doesn't read SP_* from its own environment for its own
	// config. The names it sets (SP_RUNMODE, SP_DB_TYPE, SP_NODE_ROLE, ...)
	// are real server config the child process binds anyway, so excluding
	// this directory is a scope trim, not a coverage gap.
	"cmd/membench": "sets env for child server processes, not a config reader",
	// Vendored third-party code; not ours to allowlist for.
	"third_party": "vendored third-party code",
}

// sourceScanExceptions are SP_* names the scan will find that are
// deliberately NOT server config, so they must never be added to the runtime
// allowlist (that would defeat the "unrecognized = ignored" check for a real
// operator typo). Each entry documents why.
var sourceScanExceptions = map[string]string{
	// CI-only switch that turns a skipped Postgres test into a failure; read
	// exclusively from internal/testsupport, which is imported only from
	// _test.go files (see its package doc). Never read by the running server.
	"SP_TEST_REQUIRE_POSTGRES": "internal/testsupport/postgres.go — test harness only",
}

// spLiteral is one SP_*-shaped string literal found in the source tree.
type spLiteral struct {
	name string
	pos  string // file:line
}

// scanServerSourceForSPLiterals walks the server module for every SP_*-shaped
// string literal in non-test, non-excluded Go source.
func scanServerSourceForSPLiterals(t *testing.T, root string) []spLiteral {
	t.Helper()

	var found []spLiteral

	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			rel, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}

			if rel != "." {
				if _, excluded := sourceScanExcludedDirs[filepath.ToSlash(rel)]; excluded {
					return filepath.SkipDir
				}

				base := filepath.Base(rel)
				if base == ".git" || base == "node_modules" {
					return filepath.SkipDir
				}
			}

			return nil
		}

		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, parseErr := parser.ParseFile(fset, path, nil, 0)
		if parseErr != nil {
			return fmt.Errorf("parsing %s: %w", path, parseErr)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}

			value, unquoteErr := strconv.Unquote(lit.Value)
			if unquoteErr != nil {
				return true
			}

			if spNameLiteral.MatchString(value) {
				found = append(found, spLiteral{
					name: value,
					pos:  fmt.Sprintf("%s:%d", relOrPath(root, fset.Position(lit.Pos()).Filename), fset.Position(lit.Pos()).Line),
				})
			}

			return true
		})

		return nil
	})
	require.NoError(t, err)

	return found
}

// relOrPath returns path relative to root when possible, for shorter failure
// messages; it falls back to the original path.
func relOrPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}

	return rel
}

// TestSourceScanRecognizesEveryManuallyReadSPVar walks the server module for
// every SP_*-shaped string literal — covering both os.Getenv/os.LookupEnv call
// sites and standalone name constants — and asserts each one is in
// recognizedEnvVars(). This is the regression test for the class of bug fixed
// here: a manual SP_* reader added to internal/app or internal/jobs/jobtypes
// without a matching entry in otherManualReaderEnvVars(), which made envcheck
// warn about a variable the server genuinely reads (SP_ENTITLEMENTS_BILLING_
// UPGRADE_TOKEN_SECRET, SP_SYSTEM_AGENT_ENROLLMENT_TOKENS and others — see
// TestSourceScanFindsKnownGapsPositiveControl).
func TestSourceScanRecognizesEveryManuallyReadSPVar(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	root, err := filepath.Abs("../..")
	r.NoError(err)
	_, statErr := os.Stat(filepath.Join(root, "go.mod"))
	r.NoError(statErr, "expected %s to be the server module root (go.mod not found) — "+
		"has internal/envcheck moved?", root)

	literals := scanServerSourceForSPLiterals(t, root)
	r.NotEmpty(literals, "scan found zero SP_* literals — the walker is almost certainly broken "+
		"(wrong root or nothing parsed), see the positive control below for what it must find")

	recognized := recognizedEnvVars()

	byName := make(map[string][]string) // name -> file:line occurrences
	for _, lit := range literals {
		byName[lit.name] = append(byName[lit.name], lit.pos)
	}

	var missing []string
	for name, occurrences := range byName {
		if _, ok := recognized[name]; ok {
			continue
		}
		if _, ok := sourceScanExceptions[name]; ok {
			continue
		}

		sort.Strings(occurrences)
		missing = append(missing, fmt.Sprintf("%s (%s)", name, strings.Join(occurrences, ", ")))
	}
	sort.Strings(missing)

	r.Empty(missing, "the following SP_* names are read/declared in server source but are not in "+
		"recognizedEnvVars(); add each to otherManualReaderEnvVars() in internal/envcheck/envcheck.go "+
		"(or, if it is genuinely not server config, add it to sourceScanExceptions in this file with "+
		"a comment explaining why):\n  %s", strings.Join(missing, "\n  "))
}

// TestSourceScanFindsKnownGapsPositiveControl asserts the scan actually finds
// the names at the center of this fix, so a broken walker (wrong root, zero
// files parsed, an overly broad exclusion) cannot pass
// TestSourceScanRecognizesEveryManuallyReadSPVar vacuously.
func TestSourceScanFindsKnownGapsPositiveControl(t *testing.T) {
	t.Parallel()

	r := require.New(t)

	root, err := filepath.Abs("../..")
	r.NoError(err)

	literals := scanServerSourceForSPLiterals(t, root)

	names := make(map[string]struct{}, len(literals))
	for _, lit := range literals {
		names[lit.name] = struct{}{}
	}

	r.Contains(names, "SP_ENTITLEMENTS_BILLING_UPGRADE_TOKEN_SECRET")
	r.Contains(names, "SP_SYSTEM_AGENT_ENROLLMENT_TOKENS")
	r.Contains(names, "SP_SUPPORT_RETENTION_DAYS")
	r.Contains(names, "SP_TEST_PG_BINARIES_PATH")
	// The exception itself must still be found by the walker (it is filtered
	// out by name in the other test, not by never being seen).
	r.Contains(names, "SP_TEST_REQUIRE_POSTGRES")
}
