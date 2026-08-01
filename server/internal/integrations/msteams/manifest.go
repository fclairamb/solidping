package msteams

import (
	"archive/zip"
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/url"
	"strings"
)

// colorIconPNG is the 192×192 color icon shipped in the app package. It is
// the same mark as the dashboard favicon so the app is recognizable in the
// Teams app list.
//
//go:embed assets/color.png
var colorIconPNG []byte

// Manifest constants. The schema version is pinned rather than tracking
// "latest" so a Microsoft schema change cannot silently invalidate the
// generated package.
const (
	manifestSchema  = "https://developer.microsoft.com/en-us/json-schemas/teams/v1.16/MicrosoftTeams.schema.json"
	manifestVersion = "1.16"
	appVersion      = "1.0.0"
	packageName     = "io.solidping.teams"
	appShortName    = "SolidPing"
	appFullName     = "SolidPing monitoring"
	accentColor     = "#1F6FEB"
	outlineIconSize = 32

	// scopeTeam / scopeGroupChat are the Teams install scopes the bot
	// declares. Personal scope is phase 2 (see the spec's capability map).
	scopeTeam      = "team"
	scopeGroupChat = "groupChat"

	// cmdHintChecksList is the compose-box hint for the checks-list command;
	// the same literal is asserted in the parser tests.
	cmdHintChecksList = "checks list"
)

// Manifest is the Teams app manifest, narrowed to the fields we emit.
type Manifest struct {
	Schema          string            `json:"$schema"`
	ManifestVersion string            `json:"manifestVersion"`
	Version         string            `json:"version"`
	ID              string            `json:"id"`
	PackageName     string            `json:"packageName"`
	Developer       ManifestDeveloper `json:"developer"`
	Icons           ManifestIcons     `json:"icons"`
	Name            ManifestText      `json:"name"`
	Description     ManifestText      `json:"description"`
	AccentColor     string            `json:"accentColor"`
	Bots            []ManifestBot     `json:"bots"`
	Permissions     []string          `json:"permissions"`
	ValidDomains    []string          `json:"validDomains"`
}

// ManifestDeveloper is the manifest's developer block.
type ManifestDeveloper struct {
	Name          string `json:"name"`
	WebsiteURL    string `json:"websiteUrl"`
	PrivacyURL    string `json:"privacyUrl"`
	TermsOfUseURL string `json:"termsOfUseUrl"`
}

// ManifestIcons names the icon files inside the package zip.
type ManifestIcons struct {
	Color   string `json:"color"`
	Outline string `json:"outline"`
}

// ManifestText is the short/full pair Teams uses for name and description.
type ManifestText struct {
	Short string `json:"short"`
	Full  string `json:"full"`
}

// ManifestBot describes the bot registration.
type ManifestBot struct {
	BotID              string                `json:"botId"`
	Scopes             []string              `json:"scopes"`
	SupportsFiles      bool                  `json:"supportsFiles"`
	IsNotificationOnly bool                  `json:"isNotificationOnly"`
	CommandLists       []ManifestCommandList `json:"commandLists,omitempty"`
}

// ManifestCommandList is the per-scope command hint list Teams shows in the
// compose box.
type ManifestCommandList struct {
	Scopes   []string          `json:"scopes"`
	Commands []ManifestCommand `json:"commands"`
}

// ManifestCommand is one command hint.
type ManifestCommand struct {
	Title       string `json:"title"`
	Description string `json:"description"`
}

// BuildManifest renders the app manifest for this instance. appID is the
// Entra application (client) ID — it doubles as the manifest id and the bot
// id, which is what Teams expects for a bot-only app. baseURL is the
// instance's public URL; its host becomes the single valid domain.
func BuildManifest(appID, baseURL string) *Manifest {
	site := strings.TrimSuffix(baseURL, "/")

	return &Manifest{
		Schema:          manifestSchema,
		ManifestVersion: manifestVersion,
		Version:         appVersion,
		ID:              appID,
		PackageName:     packageName,
		Developer: ManifestDeveloper{
			Name:          appShortName,
			WebsiteURL:    site,
			PrivacyURL:    site + "/docs/legal/privacy",
			TermsOfUseURL: site + "/docs/legal/terms",
		},
		Icons: ManifestIcons{Color: "color.png", Outline: "outline.png"},
		Name:  ManifestText{Short: appShortName, Full: appFullName},
		Description: ManifestText{
			Short: "Uptime monitoring alerts and check management in Teams.",
			Full: "SolidPing posts incident notifications into your Teams channels and lets you " +
				"manage checks and review incidents by mentioning the bot.",
		},
		AccentColor: accentColor,
		Bots: []ManifestBot{{
			BotID: appID,
			// team + groupChat only: personal-scope DMs are phase 2 (see the
			// spec's capability map), and declaring a scope the bot does not
			// implement would surface a dead "Chat" tab in Teams.
			Scopes:             []string{scopeTeam, scopeGroupChat},
			SupportsFiles:      false,
			IsNotificationOnly: false,
			CommandLists: []ManifestCommandList{{
				Scopes: []string{scopeTeam, scopeGroupChat},
				Commands: []ManifestCommand{
					{Title: "help", Description: "Show the available commands"},
					{Title: cmdHintChecksList, Description: "List the organization's checks"},
					{Title: "checks add <url>", Description: "Start monitoring a URL"},
					{Title: "checks rm <slug>", Description: "Stop monitoring a check"},
					{Title: "incidents list", Description: "List recent incidents"},
					{Title: "config default-channel", Description: "Send notifications to this channel"},
				},
			}},
		}},
		Permissions:  []string{"identity", "messageTeamMembers"},
		ValidDomains: validDomainsFor(site),
	}
}

// validDomainsFor extracts the host of the instance URL. Teams rejects a
// manifest whose validDomains contains a scheme or a port-less mismatch, so
// the host is taken from a parsed URL rather than string-trimmed.
func validDomainsFor(site string) []string {
	parsed, err := url.Parse(site)
	if err != nil || parsed.Hostname() == "" {
		return []string{}
	}

	return []string{parsed.Hostname()}
}

// BuildAppPackage renders the complete Teams app package: a zip holding
// manifest.json plus the two required icons. Serving this from the server is
// what makes setup paste-free — the operator never edits JSON by hand.
func BuildAppPackage(appID, baseURL string) ([]byte, error) {
	manifest := BuildManifest(appID, baseURL)

	manifestJSON, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshaling teams manifest: %w", err)
	}

	outline, err := buildOutlineIcon()
	if err != nil {
		return nil, err
	}

	var buf bytes.Buffer

	writer := zip.NewWriter(&buf)

	entries := []struct {
		name string
		data []byte
	}{
		{"manifest.json", manifestJSON},
		{"color.png", colorIconPNG},
		{"outline.png", outline},
	}

	for i := range entries {
		entry := &entries[i]

		file, createErr := writer.Create(entry.name)
		if createErr != nil {
			return nil, fmt.Errorf("creating %s in app package: %w", entry.name, createErr)
		}

		if _, writeErr := file.Write(entry.data); writeErr != nil {
			return nil, fmt.Errorf("writing %s in app package: %w", entry.name, writeErr)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("finalizing teams app package: %w", err)
	}

	return buf.Bytes(), nil
}

// buildOutlineIcon renders the 32×32 outline icon Teams shows in the app bar.
// Teams requires it to be white-on-transparent, so it is generated rather
// than shipped as a second copy of the colored mark (which Teams renders as
// a muddy blob). A filled disc is the simplest shape that reads correctly at
// 32 px.
func buildOutlineIcon() ([]byte, error) {
	img := image.NewNRGBA(image.Rect(0, 0, outlineIconSize, outlineIconSize))

	const (
		center = float64(outlineIconSize) / 2
		radius = float64(outlineIconSize)/2 - 2
	)

	white := color.NRGBA{R: 255, G: 255, B: 255, A: 255}

	for row := range outlineIconSize {
		for col := range outlineIconSize {
			dx := float64(col) + 0.5 - center
			dy := float64(row) + 0.5 - center

			if dx*dx+dy*dy <= radius*radius {
				img.SetNRGBA(col, row, white)
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encoding teams outline icon: %w", err)
	}

	return buf.Bytes(), nil
}
