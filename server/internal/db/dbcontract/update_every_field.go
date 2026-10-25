package dbcontract

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/db/models"
)

// contractUID is a well-formed UUID that matches no row: every column type
// (uuid, varchar, text) accepts it, so a SET clause can never fail on the
// literal itself.
const contractUID = "00000000-0000-4000-8000-000000000001"

// runUpdateEveryField calls every Update* method whose last argument is a
// partial-update struct, once with every value field set and once with
// every "set to NULL" switch taken, against a row that does not exist. Every
// field must map to a real column on every dialect: a misspelled or missing
// column fails the statement even when no row matches, while a missing row is
// either a silent no-op or a not-found error.
func runUpdateEveryField(t *testing.T, fix *fixture) {
	t.Helper()

	value := reflect.ValueOf(fix.svc)
	called := 0

	for i := range value.NumMethod() {
		name := value.Type().Method(i).Name
		method := value.Method(i)
		sig := method.Type()

		if !strings.HasPrefix(name, "Update") || sig.NumIn() < 3 {
			continue
		}

		last := sig.In(sig.NumIn() - 1)
		structType := last
		if structType.Kind() == reflect.Pointer {
			structType = structType.Elem()
		}

		if structType.Kind() != reflect.Struct || !strings.HasSuffix(structType.Name(), "Update") {
			continue
		}

		for _, clearInner := range []bool{false, true} {
			args := []reflect.Value{reflect.ValueOf(t.Context())}
			for j := 1; j < sig.NumIn()-1; j++ {
				arg := reflect.New(sig.In(j)).Elem()
				fillValue(arg, contractUID, 1)
				args = append(args, arg)
			}

			update := reflect.New(structType)
			fillUpdate(update.Elem(), clearInner)

			if last.Kind() == reflect.Pointer {
				args = append(args, update)
			} else {
				args = append(args, update.Elem())
			}

			out := method.Call(args)
			called++

			if err, _ := out[len(out)-1].Interface().(error); err != nil {
				require.True(t, isNotFound(err), "%s (clearInner=%v): %v", name, clearInner, err)
			}
		}
	}

	require.Greater(t, called, 20, "the sweep found the Update* methods")
}

func isNotFound(err error) bool {
	if errors.Is(err, sql.ErrNoRows) {
		return true
	}

	msg := strings.ToLower(err.Error())

	return strings.Contains(msg, "not found") || strings.Contains(msg, "no rows")
}

// fillUpdate sets the fields of a partial-update struct for one of two
// passes. The value pass fills every field except the Clear* switches:
// pointers point at a filled value and other booleans are true. The clear
// pass sets only the Clear* switches and points every pointer-to-pointer at
// nil (the "set to NULL" form). Mixing the two would ask for contradictory
// assignments to one column, which no caller does.
func fillUpdate(target reflect.Value, clearPass bool) {
	for i := range target.NumField() {
		field := target.Field(i)
		if !field.CanSet() {
			continue
		}

		isClear := field.Kind() == reflect.Bool && strings.HasPrefix(target.Type().Field(i).Name, "Clear")
		isNullable := field.Kind() == reflect.Pointer && field.Type().Elem().Kind() == reflect.Pointer

		switch {
		case clearPass && isClear:
			field.SetBool(true)
		case clearPass && isNullable:
			field.Set(reflect.New(field.Type().Elem()))
		case !clearPass && !isClear:
			fillValue(field, stringFor(target.Type().Field(i).Name), 0)
		}
	}
}

// stringFor picks the string a field is filled with: a UUID for a reference
// (uuid columns reject anything else), a one-letter value otherwise (some
// columns are short varchars).
func stringFor(name string) string {
	if strings.HasSuffix(name, "UID") || strings.HasSuffix(name, "UIDs") || strings.HasSuffix(name, "By") {
		return contractUID
	}

	return "x"
}

func fillValue(field reflect.Value, str string, depth int) {
	//nolint:exhaustive // the remaining kinds never appear in an update struct
	switch field.Kind() {
	case reflect.Pointer:
		inner := reflect.New(field.Type().Elem())
		fillValue(inner.Elem(), str, depth+1)
		field.Set(inner)
	case reflect.String:
		field.SetString(str)
	case reflect.Bool:
		field.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if field.Type() == reflect.TypeFor[time.Duration]() {
			field.SetInt(int64(time.Minute))
		} else {
			field.SetInt(1)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		field.SetUint(1)
	case reflect.Float32, reflect.Float64:
		field.SetFloat(1)
	case reflect.Map:
		field.Set(reflect.MakeMap(field.Type()))
	case reflect.Slice:
		field.Set(reflect.MakeSlice(field.Type(), 0, 0))
	case reflect.Struct:
		switch {
		case field.Type() == reflect.TypeFor[time.Time]():
			field.Set(reflect.ValueOf(time.Now().UTC()))
		case field.Type() == reflect.TypeFor[models.JSONMap]():
		case depth < 3:
			fillUpdate(field, false)
		}
	default:
	}
}
