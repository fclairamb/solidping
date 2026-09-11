package models

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// LabelKeyPatternSource is THE label key rule, spelled once. It is what the
// Postgres `labels_key_check` CHECK has always enforced and what the dashboard
// enforces on authoring (web/dash0 label-shared.ts KEY_REGEX): lowercase,
// starts with a letter, 3-51 characters, letters/digits/hyphens only.
//
// Before spec 2026-09-10-01 there were three different rules — this one, a
// laxer import/document regex that accepted 1-2 char keys, leading digits and
// dots, and a SQLite CHECK that accepted anything up to 50 characters. A
// document that passed the import validator could therefore be refused by
// Postgres at write time, with a raw SQLSTATE for a message. There is now one
// rule, checked in Go before any write, and mirrored by both backends' CHECK.
const LabelKeyPatternSource = `^[a-z][a-z0-9-]{2,50}$`

// LabelKeyPattern is the compiled LabelKeyPatternSource.
var LabelKeyPattern = regexp.MustCompile(LabelKeyPatternSource)

// LabelValueMaxLen caps a label value, matching the `labels.value` CHECK on
// both backends and the dashboard's VALUE_MAX.
const LabelValueMaxLen = 200

// labelKeyRuleHint is the human half of the key error: the regex alone tells a
// user nothing about WHY "os" or "k8s.cluster" was refused.
const labelKeyRuleHint = "(lowercase, starts with a letter, 3-51 chars, letters/digits/hyphens)"

// Label validation errors. The API layer maps both onto VALIDATION_ERROR, and
// import reports them per item — neither may ever reach a caller as a driver
// error.
var (
	// ErrLabelKeyInvalid is returned for a key no label may carry.
	ErrLabelKeyInvalid = errors.New("label key is invalid")
	// ErrLabelValueInvalid is returned for an empty or over-long value.
	ErrLabelValueInvalid = errors.New("label value is invalid")
)

// ValidateLabelKey checks one label key against the canonical rule. The
// message names both the offending key and the rule, so a 47-line import
// report tells the operator what to fix without reading the source.
func ValidateLabelKey(key string) error {
	if LabelKeyPattern.MatchString(key) {
		return nil
	}

	return fmt.Errorf("%w: %q: must match %s %s",
		ErrLabelKeyInvalid, key, LabelKeyPatternSource, labelKeyRuleHint)
}

// ValidateLabelValue checks one label value. The key is carried into the
// message because a value error with no key is unactionable on a check that
// carries a dozen labels.
//
// Empty is refused even though the Postgres CHECK only bounds the length: an
// empty value is indistinguishable from "label absent" everywhere it is read
// (selectors, filters, the dashboard), so accepting it would store a label
// that can never be matched.
func ValidateLabelValue(key, value string) error {
	if value != "" && len(value) <= LabelValueMaxLen {
		return nil
	}

	return fmt.Errorf("%w: %q: must be non-empty and at most %d characters",
		ErrLabelValueInvalid, key, LabelValueMaxLen)
}

// ValidateLabels checks a whole label map and returns the FIRST problem in
// sorted-key order. Sorted rather than map order so the same document always
// produces the same message — an import report that reshuffles its errors
// between runs is not a report anybody can diff.
func ValidateLabels(labels map[string]string) error {
	if len(labels) == 0 {
		return nil
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	for _, key := range keys {
		if err := ValidateLabelKey(key); err != nil {
			return err
		}

		if err := ValidateLabelValue(key, labels[key]); err != nil {
			return err
		}
	}

	return nil
}

// IsLabelValidationError reports whether err is one of the two label
// validation errors — the single predicate the HTTP layer uses to answer 400
// rather than 500.
func IsLabelValidationError(err error) bool {
	return errors.Is(err, ErrLabelKeyInvalid) || errors.Is(err, ErrLabelValueInvalid)
}
