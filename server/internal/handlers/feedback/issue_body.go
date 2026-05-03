// Package feedback implements the in-app bug-report flow: it accepts a
// multipart submission from the dash0 / status0 frontends, persists the
// optional screenshot via the files service, and asynchronously creates a
// GitHub issue with all the diagnostic context embedded.
package feedback

import (
	"fmt"
	"strings"
	"time"
)

// ContextPayload is the structured context captured by the frontend at submit
// time. All fields are optional — a malformed JSON blob from the frontend
// degrades to "Unknown" rendered cells, never blocks submission.
type ContextPayload struct {
	UserAgent     string   `json:"userAgent,omitempty"`
	Viewport      string   `json:"viewport,omitempty"`
	PixelRatio    float64  `json:"pixelRatio,omitempty"`
	Platform      string   `json:"platform,omitempty"`
	Language      string   `json:"language,omitempty"`
	Build         string   `json:"build,omitempty"`
	RecentErrors  []string `json:"recentErrors,omitempty"`
}

// IssueInput bundles everything buildIssueBody needs. Kept as a struct so
// adding fields doesn't churn every test signature.
type IssueInput struct {
	URL            string
	Comment        string
	OrgSlug        string
	UserEmail      string
	ServerVersion  string
	GitHash        string
	FrontendBuild  string
	Context        ContextPayload
	FileUID        string
	SignedURL      string
	SignedURLExp   time.Time
	MimeType       string
	ReportedAt     time.Time
}

// BuildIssueTitle returns the issue title: "Bug report: <first 60 chars>".
// Falls back to the URL path if no comment was given.
func BuildIssueTitle(in *IssueInput) string {
	const maxLen = 60

	subject := strings.TrimSpace(in.Comment)
	if subject == "" {
		subject = in.URL
	}

	if subject == "" {
		subject = "(no description)"
	}

	subject = strings.ReplaceAll(subject, "\n", " ")
	if len(subject) > maxLen {
		subject = subject[:maxLen] + "…"
	}

	return "Bug report: " + subject
}

// BuildIssueBody renders the markdown body. Branches on MimeType to use the
// right "Screenshot" vs "Screen recording" heading.
func BuildIssueBody(in *IssueInput) string {
	var sb strings.Builder

	sb.WriteString("## Report\n\n")
	if in.Comment != "" {
		sb.WriteString(in.Comment)
	} else {
		sb.WriteString("_(no description)_")
	}

	sb.WriteString("\n\n## Page\n\n")
	sb.WriteString(in.URL)

	sb.WriteString("\n\n## Context\n\n")
	sb.WriteString("| Field | Value |\n|-------|-------|\n")
	writeContextRow(&sb, "Organization", in.OrgSlug)
	writeContextRow(&sb, "User", in.UserEmail)
	writeContextRow(&sb, "Server Version", joinNonEmpty(in.ServerVersion, in.GitHash, " "))
	writeContextRow(&sb, "Frontend Version", in.FrontendBuild)
	writeContextRow(&sb, "Browser", in.Context.UserAgent)
	writeContextRow(&sb, "Viewport", in.Context.Viewport)

	if in.Context.PixelRatio > 0 {
		writeContextRow(&sb, "Pixel Ratio", fmt.Sprintf("%g", in.Context.PixelRatio))
	}

	writeContextRow(&sb, "Platform", in.Context.Platform)
	writeContextRow(&sb, "Language", in.Context.Language)
	writeContextRow(&sb, "Reported At", in.ReportedAt.UTC().Format(time.RFC3339))

	if len(in.Context.RecentErrors) > 0 {
		sb.WriteString("\n## Console Errors\n\n```\n")

		for _, line := range in.Context.RecentErrors {
			sb.WriteString(strings.ReplaceAll(line, "```", "ʼʼʼ"))
			sb.WriteString("\n")
		}

		sb.WriteString("```\n")
	}

	if in.SignedURL != "" {
		mediaSection(&sb, in)
	}

	sb.WriteString("\n---\n*Auto-generated from in-app bug report*\n")

	return sb.String()
}

// mediaSection adds the Screenshot/Recording block. For images we inline; for
// videos we drop the bare URL so GitHub's auto-player picks it up, plus a
// download fallback link.
func mediaSection(sb *strings.Builder, in *IssueInput) {
	if strings.HasPrefix(in.MimeType, "video/") {
		sb.WriteString("\n## Screen recording\n\n")
		sb.WriteString(in.SignedURL)
		sb.WriteString("\n\n[Download](")
		sb.WriteString(in.SignedURL)
		sb.WriteString(")\n")
	} else {
		sb.WriteString("\n## Screenshot\n\n![Screenshot](")
		sb.WriteString(in.SignedURL)
		sb.WriteString(")\n")
	}

	sb.WriteString(fmt.Sprintf(
		"\n<sub>File UID: `%s` · link valid until %s</sub>\n",
		in.FileUID,
		in.SignedURLExp.UTC().Format(time.RFC3339),
	))
}

func writeContextRow(sb *strings.Builder, key, value string) {
	if value == "" {
		value = "_unknown_"
	}

	sb.WriteString("| ")
	sb.WriteString(key)
	sb.WriteString(" | ")
	sb.WriteString(value)
	sb.WriteString(" |\n")
}

func joinNonEmpty(left, right, sep string) string {
	switch {
	case left != "" && right != "":
		return left + sep + right
	case left != "":
		return left
	default:
		return right
	}
}
