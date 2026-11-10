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
)

// SealedSecrets is the sealedSecrets object of the check write API.
type SealedSecrets struct {
	Keys         []string `json:"keys"`
	Fingerprints []string `json:"fingerprints"`
	CT           string   `json:"ct"`
}

// SealSecrets seals a secret map to age recipients and returns the
// sealedSecrets object to send with a check write.
func SealSecrets(recipients []string, secrets map[string]any) (*SealedSecrets, error) {
	if len(secrets) == 0 {
		return nil, ErrSealNoSecrets
	}

	fingerprints, ciphertext, err := credentials.SealForClient(recipients, secrets)
	if err != nil {
		return nil, err
	}

	keys := make([]string, 0, len(secrets))
	for key := range secrets {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return &SealedSecrets{Keys: keys, Fingerprints: fingerprints, CT: ciphertext}, nil
}

// SealCommand is `solidping seal`: read a JSON object of secret fields on
// stdin, fetch the recipients of the regions, print the sealedSecrets object.
func SealCommand() *cli.Command {
	return &cli.Command{
		Name:      "seal",
		Usage:     "Seal check secrets for the agents of private regions and print the sealedSecrets object",
		UsageText: "solidping seal --server https://ping.acme.com --org acme --region @dc1 < secrets.json",
		Description: "Reads a JSON object of secret fields on stdin (e.g. {\"password\": \"…\"}), fetches the " +
			"recipients of the regions from /orgs/<org>/sealing-recipients, seals the object to them with age " +
			"and prints {\"keys\", \"fingerprints\", \"ct\"}: send it as `sealedSecrets` on a check write or in " +
			"a config-as-code document. The secrets never leave this machine in plaintext.",
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

	secrets, err := readSecrets(stdin)
	if err != nil {
		return err
	}

	recipients, err := fetchRecipients(ctx, server, cliCtx.Config.Org, token, regionList)
	if err != nil {
		return err
	}

	sealed, err := SealSecrets(recipients, secrets)
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

	return secrets, nil
}

// fetchRecipients reads GET /api/v1/orgs/:org/sealing-recipients.
func fetchRecipients(ctx context.Context, server, org, token string, regionList []string) ([]string, error) {
	endpoint := strings.TrimRight(server, "/") + "/api/v1/orgs/" + url.PathEscape(org) +
		"/sealing-recipients?region=" + url.QueryEscape(strings.Join(regionList, ","))

	ctx, cancel := context.WithTimeout(ctx, sealRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build recipients request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch recipients: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxRecipientsBody))
	if err != nil {
		return nil, fmt.Errorf("read recipients: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w (status %d): %s", ErrSealServerRefused, resp.StatusCode, strings.TrimSpace(string(body)))
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
