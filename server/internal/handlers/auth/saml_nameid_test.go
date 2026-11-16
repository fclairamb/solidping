package auth

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/crewjam/saml"
	"github.com/stretchr/testify/require"
)

// Regression tests for the GO-2026-6909 cross-check (validateNameIDNamespace):
// crewjam/saml reads Subject.NameID with the bare tag xml:"NameID", which
// matches a <NameID> in ANY namespace. mapAssertion trusts that value (provider
// link id, email fallback), so a signature-valid assertion carrying a
// foreign-namespace NameID must not survive validation.
//
// The guard is a pure function of (the POSTed form, the extracted assertion):
// signature/conditions validation happens upstream in ParseResponse, so these
// tests drive it directly with crafted raw XML. The positive control for the
// full flow is TestSAMLHandleACS_HappyPath (a real signed assertion from the
// fake IdP must still pass through the guard untouched).

const (
	samlTestResponseOpen = `<samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" ` +
		`xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion">`
	samlTestResponseClose = `</samlp:Response>`
)

// samlNameIDRequest builds an ACS POST whose SAMLResponse is rawXML, the way
// the HTTP-POST binding carries it (base64 form field).
func samlNameIDRequest(t *testing.T, rawXML string) *http.Request {
	t.Helper()

	body := url.Values{
		"SAMLResponse": {base64.StdEncoding.EncodeToString([]byte(rawXML))},
		"RelayState":   {"unused"},
	}

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://localhost:4000/api/v1/auth/saml/acs", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, req.ParseForm())

	return req
}

// assertionWithNameID is what crewjam/saml hands back after ParseResponse:
// only the extracted NameID value matters to the guard.
func assertionWithNameID(value string) *saml.Assertion {
	return &saml.Assertion{Subject: &saml.Subject{NameID: &saml.NameID{Value: value}}}
}

// TestValidateNameIDNamespace is the attack/defense matrix of the cross-check.
func TestValidateNameIDNamespace(t *testing.T) {
	t.Parallel()

	const spoofed = "victim@acme.com"

	cases := []struct {
		name      string
		rawXML    string
		assertion *saml.Assertion
		wantErr   bool
	}{
		{
			// THE attack: no <saml:NameID>, a foreign-namespace <NameID>
			// inside <saml:Subject>. crewjam extracts the foreign value;
			// the namespace-aware parse sees no saml:NameID → refuse.
			name: "foreign namespace NameID is refused",
			rawXML: samlTestResponseOpen +
				`<saml:Assertion><saml:Subject>` +
				`<NameID xmlns="urn:foreign-namespace">` + spoofed + `</NameID>` +
				`</saml:Subject></saml:Assertion>` + samlTestResponseClose,
			assertion: assertionWithNameID(spoofed),
			wantErr:   true,
		},
		{
			name: "matching saml NameID passes",
			rawXML: samlTestResponseOpen +
				`<saml:Assertion><saml:Subject><saml:NameID>` + spoofed +
				`</saml:NameID></saml:Subject></saml:Assertion>` + samlTestResponseClose,
			assertion: assertionWithNameID(spoofed),
			wantErr:   false,
		},
		{
			name: "differing saml NameID is refused",
			rawXML: samlTestResponseOpen +
				`<saml:Assertion><saml:Subject><saml:NameID>someone-else@acme.com` +
				`</saml:NameID></saml:Subject></saml:Assertion>` + samlTestResponseClose,
			assertion: assertionWithNameID(spoofed),
			wantErr:   true,
		},
		{
			// A foreign NameID NEXT TO the real one must not win either:
			// the extracted value has to equal the saml-namespaced one.
			name: "foreign NameID alongside the real one does not substitute",
			rawXML: samlTestResponseOpen +
				`<saml:Assertion><saml:Subject>` +
				`<saml:NameID>real@acme.com</saml:NameID>` +
				`<NameID xmlns="urn:foreign-namespace">` + spoofed + `</NameID>` +
				`</saml:Subject></saml:Assertion>` + samlTestResponseClose,
			assertion: assertionWithNameID(spoofed),
			wantErr:   true,
		},
		{
			name: "any matching assertion in a multi-assertion response passes",
			rawXML: samlTestResponseOpen +
				`<saml:Assertion><saml:Subject><saml:NameID>first@acme.com</saml:NameID></saml:Subject></saml:Assertion>` +
				`<saml:Assertion><saml:Subject><saml:NameID>` + spoofed +
				`</saml:NameID></saml:Subject></saml:Assertion>` + samlTestResponseClose,
			assertion: assertionWithNameID(spoofed),
			wantErr:   false,
		},
		{
			// Encrypted assertions: the plaintext is not inspectable here,
			// so the cross-check skips rather than inventing a rejection.
			name: "encrypted assertion is skipped",
			rawXML: samlTestResponseOpen +
				`<saml:EncryptedAssertion>` +
				`<xenc:EncryptedData xmlns:xenc="http://www.w3.org/2001/04/xmlenc#"/>` +
				`</saml:EncryptedAssertion>` + samlTestResponseClose,
			assertion: assertionWithNameID(spoofed),
			wantErr:   false,
		},
		{
			name:      "no extracted NameID is skipped",
			rawXML:    samlTestResponseOpen + samlTestResponseClose,
			assertion: &saml.Assertion{Subject: &saml.Subject{}},
			wantErr:   false,
		},
		{
			name:      "nil subject is skipped",
			rawXML:    samlTestResponseOpen + samlTestResponseClose,
			assertion: &saml.Assertion{},
			wantErr:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateNameIDNamespace(samlNameIDRequest(t, tc.rawXML), tc.assertion)
			if tc.wantErr {
				require.ErrorIs(t, err, ErrSAMLNameIDNamespace)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// TestValidateNameIDNamespaceUninspectableResponse pins the fail-open side of
// the contract: when the raw response cannot be inspected (absent field,
// undecodable base64, unparseable XML), the guard defers to ParseResponse,
// which has already validated the assertion. It must not add rejection
// reasons of its own for responses it cannot see into.
func TestValidateNameIDNamespaceUninspectableResponse(t *testing.T) {
	t.Parallel()

	assertion := assertionWithNameID("user@acme.com")

	// No SAMLResponse field at all.
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://localhost:4000/api/v1/auth/saml/acs", http.NoBody)
	require.NoError(t, validateNameIDNamespace(req, assertion))

	// Undecodable base64.
	badBase64 := url.Values{"SAMLResponse": {"!!!not-base64!!!"}}
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://localhost:4000/api/v1/auth/saml/acs", strings.NewReader(badBase64.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, req.ParseForm())
	require.NoError(t, validateNameIDNamespace(req, assertion))

	// Decodable but not XML.
	notXML := url.Values{"SAMLResponse": {base64.StdEncoding.EncodeToString([]byte("this is not xml"))}}
	req = httptest.NewRequestWithContext(t.Context(), http.MethodPost,
		"http://localhost:4000/api/v1/auth/saml/acs", strings.NewReader(notXML.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	require.NoError(t, req.ParseForm())
	require.NoError(t, validateNameIDNamespace(req, assertion))
}
