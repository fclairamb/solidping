package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
	"github.com/fclairamb/solidping/server/internal/db/models"
	"github.com/fclairamb/solidping/server/internal/sealing"
)

// Browser-sealed check secrets (spec 2026-11-08-01): the client seals the
// secret map to the agents' public keys and sends only the age ciphertext.

var (
	// ErrSealedSecretsRequirePrivateRegions is returned when sealedSecrets is
	// sent for a check with a region that is not a private region of the org.
	ErrSealedSecretsRequirePrivateRegions = errors.New(
		"sealed secrets require every region of the check to be a private region of this organization")
	// ErrSecretsMustBeSealed is returned when an org in `required` browser
	// sealing mode receives plaintext secret fields.
	ErrSecretsMustBeSealed = errors.New(
		"this organization requires check secrets to be sealed in the browser: send sealedSecrets " +
			"(dash0 or `age` with the recipients of /sealing-recipients), not plaintext secret fields")
	// ErrSealedSecretsInvalid wraps every structural refusal of sealedSecrets.
	ErrSealedSecretsInvalid = errors.New("invalid sealedSecrets")
	// ErrSealingRecipientsChanged is returned when the blob was sealed to a
	// recipient set that is not the current one of the check's regions.
	ErrSealingRecipientsChanged = errors.New("sealing recipients changed")
)

// SealedSecretsRequest is a client-sealed secret map: `ct` is the base64 of
// an age file whose plaintext is the JSON object of the secret keys listed in
// `keys`, sealed to the recipients whose fingerprints are listed.
type SealedSecretsRequest struct {
	Keys         []string `json:"keys"`
	Fingerprints []string `json:"fingerprints"`
	CT           string   `json:"ct"`
}

// SealingRecipientsChangedError carries the current recipients, so the client
// can re-seal and retry without a second lookup.
type SealingRecipientsChangedError struct {
	Recipients []sealing.Recipient
}

func (e *SealingRecipientsChangedError) Error() string {
	return ErrSealingRecipientsChanged.Error() + ": seal the secrets again for the current recipients"
}

func (e *SealingRecipientsChangedError) Unwrap() error { return ErrSealingRecipientsChanged }

// invalidSealed builds a VALIDATION_ERROR-mapped refusal.
func invalidSealed(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrSealedSecretsInvalid, fmt.Sprintf(format, args...))
}

// secretFieldsForType returns the checker's declared secret keys.
func (s *Service) secretFieldsForType(ctx context.Context, orgUID, checkType string) []string {
	cfg, ok := s.parseConfigFor(ctx, orgUID, checkType)
	if !ok {
		return nil
	}

	return credentials.SecretFieldsFor(cfg)
}

// hasSecretValue reports whether a config value carries a secret (not absent,
// null, an empty string or an empty map).
func hasSecretValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
}

// secretKeysWithValues lists the secret keys carrying a value in config.
func secretKeysWithValues(config map[string]any, secretFields []string) []string {
	var out []string

	for _, key := range secretFields {
		if hasSecretValue(config[key]) {
			out = append(out, key)
		}
	}

	return out
}

// explicitlyClearedKeys lists the secret keys a patch sets to null or "".
func explicitlyClearedKeys(patch map[string]any, secretFields []string) []string {
	var out []string

	for _, key := range secretFields {
		if value, present := patch[key]; present && !hasSecretValue(value) {
			out = append(out, key)
		}
	}

	return out
}

// assertPlaintextSecretsAllowed refuses plaintext secret fields in an org whose
// browser sealing mode is `required`. A request without secret values passes.
func (s *Service) assertPlaintextSecretsAllowed(
	ctx context.Context, orgUID, checkType string, config map[string]any,
) error {
	if len(secretKeysWithValues(config, s.secretFieldsForType(ctx, orgUID, checkType))) == 0 {
		return nil
	}

	mode, err := sealing.BrowserSealingMode(ctx, s.db, orgUID)
	if err != nil {
		return err
	}

	if mode == sealing.ModeRequired {
		return ErrSecretsMustBeSealed
	}

	return nil
}

// sealedPlaceholders returns validation placeholders for the sealed keys: the
// checker's own Validate must not refuse a check whose secrets it cannot see.
func sealedPlaceholders(checkType string, keys []string) map[string]any {
	out := make(map[string]any, len(keys))
	injectSecretPlaceholders(checkType, out, keys)

	return out
}

// validateSealedSecretsShape runs the checks of spec §4 that need no database:
// keys are a non-empty subset of the secret fields, absent from the request
// config, and the ciphertext is a well-formed age file with one X25519 stanza
// per fingerprint.
func validateSealedSecretsShape(sealed *SealedSecretsRequest, secretFields []string, requestConfig map[string]any) error {
	if len(sealed.Keys) == 0 {
		return invalidSealed("keys must list at least one secret field")
	}

	seen := make(map[string]struct{}, len(sealed.Keys))

	for _, key := range sealed.Keys {
		if !slices.Contains(secretFields, key) {
			return invalidSealed("%q is not a secret field of this check type", key)
		}

		if _, dup := seen[key]; dup {
			return invalidSealed("%q is listed twice in keys", key)
		}

		seen[key] = struct{}{}
	}

	if inConfig := secretKeysWithValues(requestConfig, secretFields); len(inConfig) > 0 {
		return invalidSealed("%s must not also be sent in plaintext in config", strings.Join(inConfig, ", "))
	}

	if len(sealed.Fingerprints) == 0 {
		return invalidSealed("fingerprints must not be empty")
	}

	fps := make(map[string]struct{}, len(sealed.Fingerprints))
	for _, fp := range sealed.Fingerprints {
		if _, dup := fps[fp]; dup {
			return invalidSealed("fingerprint %q is listed twice", fp)
		}

		fps[fp] = struct{}{}
	}

	if err := credentials.ValidateSealedCiphertext(sealed.CT, len(sealed.Fingerprints)); err != nil {
		return invalidSealed("%v", err)
	}

	return nil
}

// applyClientSealing stores a client-sealed secret map on the check (spec
// §4): every region must be a private region of the org, the shape must be
// valid, and the fingerprints must equal the current recipients. effective is
// the public config; any secret field in it is dropped (the blob replaces
// them). check.Regions must already be the regions the check will have.
func (s *Service) applyClientSealing(
	ctx context.Context, check *models.Check, effective, requestConfig map[string]any, sealed *SealedSecretsRequest,
) error {
	privateRegions := filterPrivateRegions(check.Regions)
	if len(privateRegions) == 0 || len(privateRegions) != len(check.Regions) {
		return ErrSealedSecretsRequirePrivateRegions
	}

	for _, region := range privateRegions {
		owned, err := s.regions.OwnsPrivateRegion(ctx, check.OrganizationUID, region)
		if err != nil {
			return fmt.Errorf("check private region %s: %w", region, err)
		}

		if !owned {
			return ErrSealedSecretsRequirePrivateRegions
		}
	}

	secretFields := s.secretFieldsForType(ctx, check.OrganizationUID, check.Type)

	if err := validateSealedSecretsShape(sealed, secretFields, requestConfig); err != nil {
		return err
	}

	recipients, err := sealing.RecipientsForRegions(ctx, s.db, check.OrganizationUID, privateRegions)
	if err != nil {
		return err
	}

	if len(recipients) == 0 {
		return fmt.Errorf(
			"%w: enroll an agent in %s before saving credentials for a check that runs only there",
			ErrNoAgentsToSealTo, strings.Join(privateRegions, ", "))
	}

	want := sealing.SortedFingerprints(recipients)
	got := append([]string(nil), sealed.Fingerprints...)
	sort.Strings(got)

	if !slices.Equal(want, got) {
		return &SealingRecipientsChangedError{Recipients: recipients}
	}

	envelope, err := credentials.BuildClientSealedEnvelope(got, sealed.CT)
	if err != nil {
		return err
	}

	public, _ := credentials.SplitConfig(effective, secretFields)
	check.Config = public
	check.ConfigPrivate = nil
	check.ConfigSealed = &envelope

	keys := append([]string(nil), sealed.Keys...)
	sort.Strings(keys)

	keysJSON, err := json.Marshal(keys)
	if err != nil {
		return fmt.Errorf("marshal config private keys: %w", err)
	}

	keysStr := string(keysJSON)
	check.ConfigPrivateKeys = &keysStr

	return nil
}

// assertCoversStoredSecrets enforces the all-or-nothing replacement of a
// sealed blob (spec §4): the new secrets plus the keys the patch explicitly
// clears must cover every stored secret key, since the server cannot carry an
// unmentioned one over from a blob it cannot open.
func assertCoversStoredSecrets(storedKeys *string, provided, cleared []string) error {
	var missing []string

	for _, key := range parseConfigPrivateKeys(storedKeys) {
		if !slices.Contains(provided, key) && !slices.Contains(cleared, key) {
			missing = append(missing, key)
		}
	}

	if len(missing) > 0 {
		return invalidSealed(
			"the stored secrets are replaced together: also send %s (or set it to null to clear it)",
			strings.Join(missing, ", "))
	}

	return nil
}

// isBrowserSealed reports whether the check's secrets were sealed by a client.
func isBrowserSealed(check *models.Check) bool {
	return check.ConfigSealed != nil && credentials.SealedOrigin(*check.ConfigSealed) == credentials.SealOriginBrowser
}

// secretsEncryptionOf names the encryption mode of a check's secrets for the
// API: "browser", "server", or nil when it has none.
func secretsEncryptionOf(check *models.Check, privateKeys []string) *string {
	mode := ""

	switch {
	case isBrowserSealed(check):
		mode = credentials.SealOriginBrowser
	case len(privateKeys) > 0:
		mode = credentials.SealOriginServer
	default:
		return nil
	}

	return &mode
}

// sameRegionSet reports whether two region lists hold the same regions.
func sameRegionSet(left, right []string) bool {
	a := append([]string(nil), left...)
	b := append([]string(nil), right...)

	sort.Strings(a)
	sort.Strings(b)

	return slices.Equal(a, b)
}

// SealingRecipients answers GET /orgs/:org/sealing-recipients: what a blob for
// those private regions must be sealed to. A region that is not a private
// region of the org is refused.
func (s *Service) SealingRecipients(
	ctx context.Context, orgSlug string, regionList []string,
) ([]sealing.Recipient, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	if len(regionList) == 0 {
		return nil, ErrSealedSecretsRequirePrivateRegions
	}

	for _, region := range regionList {
		owned, ownErr := s.regions.OwnsPrivateRegion(ctx, org.UID, region)
		if ownErr != nil {
			return nil, ownErr
		}

		if !owned {
			return nil, ErrSealedSecretsRequirePrivateRegions
		}
	}

	return sealing.RecipientsForRegions(ctx, s.db, org.UID, regionList)
}

// NonCompliantCheck is a check that does not meet the `required` mode: it has
// secrets the server encrypted, or secrets on a shared or mixed region set.
type NonCompliantCheck struct {
	UID     string   `json:"uid"`
	Slug    string   `json:"slug"`
	Name    string   `json:"name"`
	Regions []string `json:"regions"`
	Reason  string   `json:"reason"`
}

// Reasons a check does not meet the `required` mode.
const (
	nonCompliantSharedRegion = "sharedRegion"
	nonCompliantServerSealed = "serverEncrypted"
)

// ListNonCompliantChecks lists the org's checks whose secrets are not
// browser-sealed (spec §1). They keep running until their next secret-bearing
// write.
func (s *Service) ListNonCompliantChecks(ctx context.Context, orgSlug string) ([]NonCompliantCheck, error) {
	org, err := s.db.GetOrganizationBySlug(ctx, orgSlug)
	if err != nil {
		return nil, ErrOrganizationNotFound
	}

	checks, _, err := s.db.ListChecks(ctx, org.UID, &models.ListChecksFilter{})
	if err != nil {
		return nil, err
	}

	out := make([]NonCompliantCheck, 0)

	for _, check := range checks {
		if check.ConfigPrivateKeys == nil || len(parseConfigPrivateKeys(check.ConfigPrivateKeys)) == 0 {
			continue
		}

		if isBrowserSealed(check) {
			continue
		}

		reason := nonCompliantServerSealed

		private := filterPrivateRegions(check.Regions)
		if len(private) == 0 || len(private) != len(check.Regions) {
			reason = nonCompliantSharedRegion
		}

		slug := ""
		if check.Slug != nil {
			slug = *check.Slug
		}

		out = append(out, NonCompliantCheck{
			UID: check.UID, Slug: slug, Name: checkDisplayName(check),
			Regions: check.Regions, Reason: reason,
		})
	}

	return out, nil
}

// applySealedOnlyReplacement encrypts a PATCH's plaintext secrets server-side
// and applies the replacement rules of a sealed-only row (spec 2026-11-08-01
// §4), whichever side sealed it:
//
//   - new plaintext secrets must cover every stored key (the server cannot
//     carry an unmentioned one over from a blob it cannot open);
//   - clearing every stored key drops the blob; clearing only some is refused;
//   - a PATCH that touches no secret keeps the blob as is.
//
// On a browser-sealed row, plaintext secrets switch the check to server-side
// encryption: applyEncryption seals or encrypts them as for any other check.
func (s *Service) applySealedOnlyReplacement(
	ctx context.Context, check *models.Check, merged map[string]any,
	secretFields, cleared []string, wasSealedOnly bool, oldSealed, oldPrivateKeys *string,
) error {
	provided := secretKeysWithValues(merged, secretFields)

	var clearedStored []string

	for _, key := range cleared {
		if slices.Contains(parseConfigPrivateKeys(oldPrivateKeys), key) {
			clearedStored = append(clearedStored, key)
		}
	}

	if wasSealedOnly && len(provided) > 0 {
		if err := assertCoversStoredSecrets(oldPrivateKeys, provided, cleared); err != nil {
			return err
		}
	}

	clearAll := false

	if wasSealedOnly && len(provided) == 0 && len(clearedStored) > 0 {
		if err := assertCoversStoredSecrets(oldPrivateKeys, nil, cleared); err != nil {
			return err
		}

		clearAll = true
	}

	if err := s.applyEncryption(ctx, check, merged); err != nil {
		return err
	}

	if !clearAll && len(provided) == 0 && check.ConfigSealed == nil && oldSealed != nil {
		check.ConfigSealed = oldSealed

		if check.ConfigPrivateKeys == nil {
			check.ConfigPrivateKeys = oldPrivateKeys
		}
	}

	return nil
}

// recipientsChangedFor builds the 409 for a region change that would strand a
// browser-sealed blob: it lists the recipients of the new regions when they
// are all private, none otherwise (a shared region cannot hold such a blob).
func (s *Service) recipientsChangedFor(ctx context.Context, orgUID string, newRegions []string) error {
	private := filterPrivateRegions(newRegions)
	if len(private) == 0 || len(private) != len(newRegions) {
		return &SealingRecipientsChangedError{}
	}

	recipients, err := sealing.RecipientsForRegions(ctx, s.db, orgUID, private)
	if err != nil {
		return err
	}

	return &SealingRecipientsChangedError{Recipients: recipients}
}
