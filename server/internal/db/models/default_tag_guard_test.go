package models_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- The `default:` guard (spec 2026-08-30-04) ------------------------------
//
// bun emits the literal `DEFAULT` in the VALUES clause for any field whose tag
// declares a `default:` and whose Go value is the zero value:
//
//	// bun@v1.2.18 query_insert.go:449
//	func (q InsertQuery) marshalsToDefault(f *schema.Field, v reflect.Value) bool {
//		return (f.IsPtr && f.HasNilValue(v)) ||
//			(f.HasZeroValue(v) && (f.NullZero || f.SQLDefault != ""))
//	}
//
// So `ShowAvailability bool` tagged `default:true` could not be CREATED as
// false by any code path — the DDL default won, silently. Three fields above
// the comment on StatusPage.AutoPublishDelaySeconds that documents this exact
// trap. A correct comment did not stop the bug being written next to it, so
// this test exists instead.
//
// Why source parsing rather than reflection over a registry: reflection needs a
// hand-maintained list of model types, and the failure mode here is a NEW field
// on a NEW model that nobody remembered to register — exactly the field the
// registry would omit. Parsing every .go file in the package cannot miss one.

// allowedDefaultTags lists `struct.Field` entries permitted to keep a
// `default:` clause whose value differs from the field's Go zero value.
//
// It is deliberately EMPTY. Every entry costs a column whose zero value the
// application can never write, so adding one is a design decision that belongs
// in a spec, with the reason spelled out in the field's own comment. The audit
// behind spec 2026-08-30-04 found 71 non-timestamp `default:` clauses and kept
// none of them: 10 were live bugs (a legal zero value the create path could not
// express), 41 were inert (`default:0` / `false` / `empty string` — agreeing with the
// zero value, and a loaded gun for whoever changes the DDL default next), and
// 20 were non-zero enum/counter defaults already supplied by the model's Go
// constructor, which is now their single source.
var allowedDefaultTags = map[string]string{
	// "StatusPage.Example": "why this one column must keep its DDL default",
}

// zeroLiterals are the SQL default literals that AGREE with a Go zero value.
// A `default:` carrying one of these is harmless at insert time (bun writes
// DEFAULT, the DDL writes the same value the Go zero would have) — it is still
// removed on sight, but it is not what this test fails on.
var zeroLiterals = map[string]bool{
	"0": true, "false": true, "''": true, `""`: true, "0.0": true,
}

type taggedField struct {
	structName string
	fieldName  string
	typeExpr   string
	bunTag     string
	pos        string
}

// TestNoBunDefaultTagShadowsAZeroValue fails when a field declares a
// `default:` whose value differs from that field's Go zero value — the shape
// that makes the zero value unwritable on create.
func TestNoBunDefaultTagShadowsAZeroValue(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	fields := bunTaggedFields(t)
	r.NotEmpty(fields, "parsed no bun-tagged fields at all — the walker is broken, not the models")

	for _, f := range fields {
		def, ok := defaultClause(f.bunTag)
		if !ok {
			continue
		}

		key := f.structName + "." + f.fieldName
		if reason, allowed := allowedDefaultTags[key]; allowed {
			r.NotEmpty(reason, "allowlist entry %s must carry a reason", key)

			continue
		}

		if def == "current_timestamp" {
			// Deliberate and correct: a zero time.Time falling back to the DDL
			// default is exactly the wanted behaviour for created_at/updated_at.
			// It is only correct on a time field, though — on anything else the
			// column would be unwritable for the same reason as the rest.
			r.Truef(isTimeType(f.typeExpr),
				"%s (%s): `default:current_timestamp` on a non-time field of type %s — "+
					"bun will drop this field from every INSERT that leaves it at its zero value",
				key, f.pos, f.typeExpr)

			continue
		}

		r.Truef(zeroLiterals[def],
			"%s (%s): bun tag declares `default:%s`, which differs from the Go zero value of %s.\n"+
				"bun emits the literal DEFAULT for a zero-valued field with a `default:` clause, so this "+
				"column CANNOT be created with its zero value by any code path — the DDL default wins "+
				"silently (spec 2026-08-30-04).\n"+
				"Drop the `default:` from the bun tag and set the default in the model's New… "+
				"constructor instead; the DDL default still covers rows inserted outside the application.",
			key, f.pos, def, f.typeExpr)
	}
}

// TestNoInertBunDefaultTagsRemain keeps the zero-agreeing clauses out too. They
// are not bugs today, but `default:false` becomes `default:true` the day
// somebody flips the DDL default, and then it is the bug above — written by an
// author who had no reason to look at this file.
func TestNoInertBunDefaultTagsRemain(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, f := range bunTaggedFields(t) {
		def, ok := defaultClause(f.bunTag)
		if !ok || !zeroLiterals[def] {
			continue
		}

		r.Failf("inert `default:` clause left in place",
			"%s.%s (%s): `default:%s` agrees with the Go zero value, so it changes nothing today — "+
				"but it arms the trap in TestNoBunDefaultTagShadowsAZeroValue for whoever changes the "+
				"DDL default next. Drop it (spec 2026-08-30-04).",
			f.structName, f.fieldName, f.pos, def)
	}
}

// bunTaggedFields walks every non-test .go file in this package and returns one
// entry per struct field carrying a `bun:"…"` tag.
func bunTaggedFields(t *testing.T) []taggedField {
	t.Helper()

	entries, err := os.ReadDir(".")
	require.NoError(t, err)

	fset := token.NewFileSet()

	var out []taggedField

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		require.NoError(t, parseErr)

		ast.Inspect(file, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}

			st, ok := spec.Type.(*ast.StructType)
			if !ok {
				return true
			}

			for _, field := range st.Fields.List {
				if field.Tag == nil {
					continue
				}

				raw, unqErr := strconv.Unquote(field.Tag.Value)
				if unqErr != nil {
					continue
				}

				bunTag, found := reflect.StructTag(raw).Lookup("bun")
				if !found {
					continue
				}

				for _, fieldName := range field.Names {
					out = append(out, taggedField{
						structName: spec.Name.Name,
						fieldName:  fieldName.Name,
						typeExpr:   exprString(field.Type),
						bunTag:     bunTag,
						pos:        fset.Position(field.Pos()).String(),
					})
				}
			}

			return true
		})
	}

	return out
}

// defaultClause returns the text after `default:` in a bun tag, if any.
func defaultClause(bunTag string) (string, bool) {
	for _, part := range strings.Split(bunTag, ",") {
		if after, found := strings.CutPrefix(part, "default:"); found {
			return after, true
		}
	}

	return "", false
}

func isTimeType(typeExpr string) bool {
	return typeExpr == "time.Time" || typeExpr == "*time.Time"
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ArrayType:
		return "[]" + exprString(v.Elt)
	case *ast.MapType:
		return "map[" + exprString(v.Key) + "]" + exprString(v.Value)
	default:
		return "?"
	}
}
