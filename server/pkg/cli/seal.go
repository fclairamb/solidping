package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/fclairamb/solidping/server/internal/crypto/credentials"
)

// Sealed secrets from the command line (spec 2026-11-10-03 §2 and §3).

const (
	flagServer         = "server"
	flagRegion         = "region"
	flagCheckType      = "type"
	flagCheckConfig    = "config-json"
	sealRequestTimeout = 30 * time.Second
	// maxSealInputBytes bounds the secrets read on stdin: a sealed age file
	// is capped at 64 KiB, so a larger plaintext could never be stored.
	maxSealInputBytes = 64 * 1024
	// maxRecipientsBody bounds the recipients response read.
	maxRecipientsBody = 1024 * 1024
)

// Errors of `solidping seal`.
var (
	ErrSealNoRegion      = errors.New("--region is required: the private region(s) the check runs in")
	ErrSealNoToken       = errors.New("no credential: pass --token / SP_TOKEN, or run `solidping client auth login`")
	ErrSealNoSecrets     = errors.New("stdin must hold a non-empty JSON object of secret fields")
	ErrSealNoRecipients  = errors.New("those regions have nothing to seal to: enroll an agent or set a region key")
	ErrSealServerRefused = errors.New("the server refused the recipients request")
	ErrSealNoType        = errors.New("--type is required: the check type the secrets are for (e.g. http)")
	ErrSealBadConfig     = errors.New("--config-json must be the check's public config, a JSON object")
	ErrSealUnknownType   = errors.New("the server does not know this check type")
)

// SealedSecrets is the sealedSecrets object of the check write API.
type SealedSecrets struct {
	Keys         []string `json:"keys"`
	Fingerprints []string `json:"fingerprints"`
	CT           string   `json:"ct"`
	// Bound says the plaintext carries a `_binding` (spec 2026-11-11-01).
	Bound bool `json:"bound,omitempty"`
}

// SealTarget is what the secrets are bound to: the check type, its target
// fields and its public config (spec 2026-11-11-01).
type SealTarget struct {
	Type         string
	TargetFields []string
	Config       map[string]any
}

// SealSecrets seals a secret map to age recipients and returns the
// sealedSecrets object to send with a check write. A non-nil target puts the
// `_binding` next to the secrets, so the agents refuse them once the check's
// URL, host, port or tunnel changes.
func SealSecrets(recipients []string, secrets map[string]any, target *SealTarget) (*SealedSecrets, error) {
	if len(secrets) == 0 {
		return nil, ErrSealNoSecrets
	}

	if _, reserved := secrets[credentials.BindingKey]; reserved {
		return nil, fmt.Errorf("%w: %s is reserved", ErrSealNoSecrets, credentials.BindingKey)
	}

	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	plain := secrets
	if target != nil {
		plain = credentials.WithBinding(secrets, target.Type, target.TargetFields, target.Config)
	}

	fingerprints, ciphertext, err := credentials.SealForClient(recipients, plain)
	if err != nil {
		return nil, err
	}

	return &SealedSecrets{Keys: keys, Fingerprints: fingerprints, CT: ciphertext, Bound: target != nil}, nil
}

// SealCommand is `solidping seal`: read a JSON object of secret fields on
// stdin, fetch the recipients of the regions, print the sealedSecrets object.
func SealCommand() *cli.Command {
	return &cli.Command{
		Name:  "seal",
		Usage: "Seal check secrets for the agents of private regions and print the sealedSecrets object",
		UsageText: "solidping seal --server https://ping.acme.com --org acme --region @dc1 " +
			"--type http --config-json '{\"url\":\"https://db.acme.com\"}' < secrets.json",
		Description: "Reads a JSON object of secret fields on stdin (e.g. {\"password\": \"…\"}), fetches the " +
			"recipients of the regions from /orgs/<org>/sealing-recipients, seals the object to them with age " +
			"and prints {\"keys\", \"fingerprints\", \"ct\", \"bound\"}: send it as `sealedSecrets` on a " +
			"check write or in a config-as-code document. The secrets never leave this machine in plaintext. " +
			"They are bound to the check's type and target (--type, and its public config in --config-json): " +
			"the agents refuse them once the URL, host, port, database or tunnel changes.",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    flagServer,
				Usage:   "Server URL (defaults to --url / the saved configuration)",
				Sources: cli.EnvVars("SP_SERVER_URL"),
			},
			&cli.StringSliceFlag{
				Name:  flagRegion,
				Usage: "Private region the check runs in, e.g. @dc1 (repeat for several)",
			},
			&cli.StringFlag{
				Name:  flagCheckType,
				Usage: "Type of the check the secrets are for, e.g. http (required: the secrets are bound to it)",
			},
			&cli.StringFlag{
				Name: flagCheckConfig,
				Usage: "Public config of the check, as a JSON object, or @file to read it from a file " +
					"(required: the secrets are bound to its URL, host, port, database and tunnel)",
			},
			&cli.StringFlag{
				Name:    flagToken,
				Aliases: []string{"t"},
				Usage:   "Personal Access Token (defaults to the saved login)",
				Sources: cli.EnvVars("SP_TOKEN"),
			},
		},
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runSeal(ctx, cmd, os.Stdin, os.Stdout)
		},
	}
}

func runSeal(ctx context.Context, cmd *cli.Command, stdin io.Reader, stdout io.Writer) error {
	regionList := cmd.StringSlice(flagRegion)
	if len(regionList) == 0 {
		return ErrSealNoRegion
	}

	cliCtx, err := NewCLIContext(cmd)
	if err != nil {
		return err
	}

	server := cmd.String(flagServer)
	if server == "" {
		server = cliCtx.Config.URL
	}

	token := cmd.String(flagToken)
	if token == "" {
		if token, err = cliCtx.APIHelper.Token(ctx); err != nil || token == "" {
			return ErrSealNoToken
		}
	}

	checkType := cmd.String(flagCheckType)
	if checkType == "" {
		return ErrSealNoType
	}

	publicConfig, err := readCheckConfig(cmd.String(flagCheckConfig))
	if err != nil {
		return err
	}

	secrets, err := readSecrets(stdin)
	if err != nil {
		return err
	}

	recipients, err := fetchRecipients(ctx, server, cliCtx.Config.Org, token, regionList)
	if err != nil {
		return err
	}

	targetFields, err := fetchTargetFields(ctx, server, cliCtx.Config.Org, token, checkType)
	if err != nil {
		return err
	}

	sealed, err := SealSecrets(recipients, secrets,
		&SealTarget{Type: checkType, TargetFields: targetFields, Config: publicConfig})
	if err != nil {
		return err
	}

	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")

	return encoder.Encode(sealed)
}

func readSecrets(stdin io.Reader) (map[string]any, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxSealInputBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read stdin: %w", err)
	}

	if len(raw) > maxSealInputBytes {
		return nil, fmt.Errorf("%w (at most 64 KiB)", ErrSealNoSecrets)
	}

	var secrets map[string]any
	if err := json.Unmarshal(raw, &secrets); err != nil || len(secrets) == 0 {
		return nil, ErrSealNoSecrets
	}

	if _, reserved := secrets[credentials.BindingKey]; reserved {
		return nil, fmt.Errorf("%w: %s is reserved", ErrSealNoSecrets, credentials.BindingKey)
	}

	return secrets, nil
}

// readCheckConfig parses --config-json: a JSON object, or @path to read one.
func readCheckConfig(value string) (map[string]any, error) {
	if value == "" {
		return nil, ErrSealBadConfig
	}

	raw := []byte(value)

	if path, isFile := strings.CutPrefix(value, "@"); isFile {
		content, err := os.ReadFile(path) //nolint:gosec // the operator names the file
		if err != nil {
			return nil, fmt.Errorf("read --config-json file: %w", err)
		}

		raw = content
	}

	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil || config == nil {
		return nil, ErrSealBadConfig
	}

	return config, nil
}

// fetchTargetFields reads the target fields of a check type from
// GET /api/v1/orgs/:org/check-types (spec 2026-11-11-01).
func fetchTargetFields(ctx context.Context, server, org, token, checkType string) ([]string, error) {
	endpoint := strings.TrimRight(server, "/") + "/api/v1/orgs/" + url.PathEscape(org) + "/check-types"

	body, err := sealGet(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data []struct {
			Type         string   `json:"type"`
			TargetFields []string `json:"targetFields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode check types: %w", err)
	}

	for _, entry := range parsed.Data {
		if entry.Type == checkType {
			if entry.TargetFields == nil {
				return credentials.TargetFieldsFor(nil), nil
			}

			return entry.TargetFields, nil
		}
	}

	return nil, fmt.Errorf("%w: %s", ErrSealUnknownType, checkType)
}

// fetchRecipients reads GET /api/v1/orgs/:org/sealing-recipients.
func fetchRecipients(ctx context.Context, server, org, token string, regionList []string) ([]string, error) {
	endpoint := strings.TrimRight(server, "/") + "/api/v1/orgs/" + url.PathEscape(org) +
		"/sealing-recipients?region=" + url.QueryEscape(strings.Join(regionList, ","))

	body, err := sealGet(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}

	var parsed struct {
		Data []struct {
			Recipient string `json:"recipient"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("decode recipients: %w", err)
	}

	out := make([]string, 0, len(parsed.Data))
	for _, entry := range parsed.Data {
		out = append(out, entry.Recipient)
	}

	if len(out) == 0 {
		return nil, ErrSealNoRecipients
	}

	return out, nil
}

// sealGet performs an authenticated GET and returns the body of a 200.
func sealGet(ctx context.Context, endpoint, token string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, sealRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s: %w", endpoint, err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRecipientsBody))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (status %d): %s", ErrSealServerRefused, resp.StatusCode, strings.TrimSpace(string(body)))
	}

	return body, nil
}

// AgentCommand is `solidping agent`. `agent region-key generate` prints a
// fresh age X25519 identity, offline. The output is a region keys file
// (SP_AGENT_REGION_KEYS_FILE): the public key and fingerprint as comments,
// then the identity.
func AgentCommand() *cli.Command {
	return &cli.Command{
		Name:  "agent",
		Usage: "Deported agent tools",
		Commands: []*cli.Command{
			{
				Name:  "region-key",
				Usage: "Shared region keys of private regions",
				Commands: []*cli.Command{
					{
						Name: "generate",
						Usage: "Print a fresh region key, offline: paste the public key in dash0 " +
							"(Private locations → Paste a public key), give the identity to the region's agents",
						Action: func(_ context.Context, _ *cli.Command) error {
							return writeRegionKey(os.Stdout)
						},
					},
				},
			},
		},
	}
}

// writeRegionKey prints a fresh identity in the region keys file format.
func writeRegionKey(out io.Writer) error {
	identity, recipient, err := credentials.GenerateX25519Keypair()
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(out, "# public key: %s\n# fingerprint: %s\n%s\n",
		recipient, credentials.RecipientFingerprint(recipient), identity)

	return err
}
