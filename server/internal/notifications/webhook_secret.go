package notifications

import (
	crypto_rand "crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// Webhook signing-secret format (Standard Webhooks).
//
// A secret is `whsec_` followed by the key in standard base64 with padding
// (`base64.StdEncoding`), which is exactly what the official Standard Webhooks
// libraries decode. Secrets created before this format was adopted were
// URL-safe base64 without padding; DecodeWebhookSecret accepts both spellings
// so every stored secret keeps decoding to the same key bytes.
const (
	// WebhookSecretPrefix is the Svix-compatible prefix for signing secrets.
	WebhookSecretPrefix = "whsec_"
	// webhookSecretBytes is the number of random bytes in a generated secret.
	webhookSecretBytes = 32
	// maxBase64Padding is the most `=` a base64 body can end with.
	maxBase64Padding = 2
)

// ErrEmptySigningSecret is wrapped (with ErrInvalidSigningSecret) when a
// secret decodes to zero key bytes.
var ErrEmptySigningSecret = errors.New("signing secret is empty")

// GenerateWebhookSecret returns a fresh `whsec_<standard-base64-32-bytes>`
// signing secret using crypto/rand. The result is always 50 characters and
// ends in a single `=`.
func GenerateWebhookSecret() (string, error) {
	raw := make([]byte, webhookSecretBytes)
	if _, err := crypto_rand.Read(raw); err != nil {
		return "", fmt.Errorf("generating webhook secret: %w", err)
	}

	return WebhookSecretPrefix + base64.StdEncoding.EncodeToString(raw), nil
}

// DecodeWebhookSecret returns the key bytes of a signing secret. It accepts an
// optional `whsec_` prefix, standard or URL-safe alphabet, with or without
// padding. Every error wraps ErrInvalidSigningSecret.
func DecodeWebhookSecret(secret string) ([]byte, error) {
	body := strings.TrimPrefix(secret, WebhookSecretPrefix)

	// Strip up to two trailing pads; any other '=' is malformed.
	for range maxBase64Padding {
		body = strings.TrimSuffix(body, "=")
	}

	if strings.Contains(body, "=") {
		return nil, fmt.Errorf("%w: unexpected padding", ErrInvalidSigningSecret)
	}

	body = strings.NewReplacer("-", "+", "_", "/").Replace(body)

	key, err := base64.RawStdEncoding.DecodeString(body)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSigningSecret, err)
	}

	if len(key) == 0 {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSigningSecret, ErrEmptySigningSecret)
	}

	return key, nil
}

// CanonicalWebhookSecret re-spells a signing secret as `whsec_` + standard
// padded base64 of the same key bytes. It is idempotent.
func CanonicalWebhookSecret(secret string) (string, error) {
	key, err := DecodeWebhookSecret(secret)
	if err != nil {
		return "", err
	}

	return WebhookSecretPrefix + base64.StdEncoding.EncodeToString(key), nil
}
