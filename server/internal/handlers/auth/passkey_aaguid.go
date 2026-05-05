package auth

// aaguidLabels maps WebAuthn AAGUIDs to human-friendly authenticator
// names. Sourced from passkeydeveloper/passkey-authenticator-aaguids;
// kept small (top vendors only) — unknown AAGUIDs fall back to
// "Security key". Add entries lazily as users surface them.
//
//nolint:gochecknoglobals // intentional package-level lookup table
var aaguidLabels = map[string]string{
	"adce0002-35bc-c60a-648b-0b25f1f05503": "Chrome on Mac",
	"08987058-cadc-4b81-b6e1-30de50dcbe96": "Windows Hello",
	"9ddd1817-af5a-4672-a2b9-3e3dd95000a9": "Windows Hello",
	"6028b017-b1d4-4c02-b4b3-afcdafc96bb2": "Windows Hello",
	"dd4ec289-e01d-41c9-bb89-70fa845d4bf2": "iCloud Keychain (Managed)",
	"fbfc3007-154e-4ecc-8c0b-6e020557d7bd": "iCloud Keychain",
	"ea9b8d66-4d01-1d21-3ce4-b6b48cb575d4": "Google Password Manager",
	"bada5566-a7aa-401f-bd96-45619a55120d": "1Password",
	"b84e4048-15dc-4dd0-8640-f4f60813c8af": "NordPass",
	"de1e552d-db1d-4423-a619-566b625cdc84": "NordPass",
	"d548826e-79b4-db40-a3d8-11116f7e8349": "Bitwarden",
	"771b48fd-d3d4-4f74-9232-fc157ab0507a": "Edge on Mac",
	"39a5647e-1853-446c-a1f6-a79bae9f5bc7": "IDmelon",
	"d384db22-4d50-ebde-2eac-5765cf1e2a44": "Firefox",
	"531126d6-e717-415c-9320-3d9aa6981239": "Dashlane",
	"f3809540-7f14-49c1-a8b3-8f813b225541": "Enpass",
	"b93fd961-f2e6-462f-b122-82002247de78": "Android",
	"50a45b0c-80e7-f944-bf29-f552bfa2e048": "ACS FIDO Authenticator",
	"ee041bce-25e5-4cdb-8f86-897fd6418464": "Feitian ePass FIDO2",
	"2fc0579f-8113-47ea-b116-bb5a8db9202a": "YubiKey 5 NFC",
	"73bb0cd4-e502-49b8-9c6f-b59445bf720b": "YubiKey 5C NFC",
	"c1f9a0bc-1dd2-404a-b27f-8e29047a43fd": "YubiKey 5 NFC FIPS",
	"c5ef55ff-ad9a-4b9f-b580-adebafe026d0": "YubiKey 5Ci",
	"b92c3f9a-c014-4056-887f-140a2501163b": "YubiKey 5 (legacy)",
	"83c47309-aabb-4108-8470-8be838b573cb": "YubiKey Bio",
	"d8522d9f-575b-4866-88a9-ba99fa02f35b": "YubiKey Bio (Enterprise)",
	"cb69481e-8ff7-4039-93ec-0a2729a154a8": "YubiKey 5 Series",
	"f8a011f3-8c0a-4d15-8006-17111f9edc7d": "Solo",
	"8876631b-d4a0-427f-5773-0ec71c9e0279": "Solo Tap",
	"0acf3011-bdb4-4757-8c34-9d6da9bd1936": "Apple Passkey (iOS)",
}

// aaguidLabel returns the friendly label or "Security key" when the
// AAGUID is unknown.
func aaguidLabel(aaguid string) string {
	if aaguid == "" {
		return "Security key"
	}

	if label, ok := aaguidLabels[aaguid]; ok {
		return label
	}

	return "Security key"
}
