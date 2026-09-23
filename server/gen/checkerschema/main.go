// Command checkerschema regenerates the per-check-type JSON Schemas served from
// internal/checkers/schemas.
//
// It reflects over the very `XConfig` structs the validators run on, enumerated
// through internal/checkers/configregistry — the same closed switch
// `sp checks validate` and the server both go through — so a new check type
// cannot be forgotten: add it to the registry and its schema appears here on the
// next `go generate`.
//
// The output is a DESCRIPTION, never a validator. See the package doc of
// internal/checkers/schemas and the `x-solidping-validation` key every generated
// file carries.
//
// Usage:
//
//	go run ./gen/checkerschema -out internal/checkers/schemas
//
// It is wired as the `go:generate` directive of internal/checkers/schemas, and
// CI fails when the committed files differ from a fresh run.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/fclairamb/solidping/server/internal/checkers/checkerdef"
	"github.com/fclairamb/solidping/server/internal/checkers/configregistry"
)

// Static errors the generator can fail with. The message always names the fix,
// because "run go generate" is the only useful thing to say to whoever hit it.
var (
	errNoCheckTypes = errors.New("configregistry knows no check type")
	errStaleSchema  = errors.New("stale — run `go generate ./internal/checkers/schemas/...` and commit the result")
	errOrphanSchema = errors.New("no matching check type — run `go generate ./internal/checkers/schemas/...`")
)

func main() {
	outDir := flag.String("out", ".", "directory the <type>.json schemas are written to")
	check := flag.Bool("check", false, "do not write; exit non-zero when a committed schema is stale")
	flag.Parse()

	if err := run(*outDir, *check); err != nil {
		log.Fatalf("checkerschema: %v", err)
	}
}

// run generates every schema and either writes it or compares it to what is on
// disk. Both modes walk the same code so -check can never disagree with a real
// generation.
func run(outDir string, checkOnly bool) error {
	types := knownTypes()
	if len(types) == 0 {
		return errNoCheckTypes
	}

	wanted := make(map[string]struct{}, len(types))

	for _, checkType := range types {
		doc, err := buildSchema(checkType)
		if err != nil {
			return fmt.Errorf("check type %q: %w", checkType, err)
		}

		name := string(checkType) + ".json"
		wanted[name] = struct{}{}
		path := filepath.Join(outDir, name)

		if checkOnly {
			if err := compareFile(path, doc); err != nil {
				return err
			}

			continue
		}

		if err := os.WriteFile(path, doc, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}

	return pruneOrReport(outDir, wanted, checkOnly)
}

// knownTypes returns every check type the light config registry implements,
// sorted so the generator's output order never depends on map iteration.
//
// checkerdef.ListCheckTypes is the declaration list and configregistry is the
// implementation switch; taking the intersection means a type declared but not
// wired gets no schema, and TestSchemaPerRegistryType pins the other direction.
func knownTypes() []checkerdef.CheckType {
	all := checkerdef.ListCheckTypes(nil)
	out := make([]checkerdef.CheckType, 0, len(all))

	for _, checkType := range all {
		if configregistry.IsKnownType(checkType) {
			out = append(out, checkType)
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })

	return out
}

// compareFile reports a stale or missing committed schema as an error naming the
// fix, because "run go generate" is the only useful thing to say here.
func compareFile(path string, want []byte) error {
	got, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%s is missing — run `go generate ./internal/checkers/schemas/...`: %w", path, err)
	}

	if !bytes.Equal(got, want) {
		return fmt.Errorf("%s is %w", path, errStaleSchema)
	}

	return nil
}

// pruneOrReport deletes schema files for types that no longer exist (or, in
// -check mode, reports them). A schema left behind for a removed check type
// would keep telling editors about a type the server rejects.
func pruneOrReport(outDir string, wanted map[string]struct{}, checkOnly bool) error {
	entries, err := filepath.Glob(filepath.Join(outDir, "*.json"))
	if err != nil {
		return fmt.Errorf("scan %s: %w", outDir, err)
	}

	for _, path := range entries {
		if _, ok := wanted[filepath.Base(path)]; ok {
			continue
		}

		if checkOnly {
			return fmt.Errorf("%s has %w", path, errOrphanSchema)
		}

		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale %s: %w", path, err)
		}
	}

	return nil
}
