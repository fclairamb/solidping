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
	UserAgent    string   `json:"userAgent,omitempty"`
	Viewport     string   `json:"viewport,omitempty"`
	PixelRatio   float64  `json:"pixelRatio,omitempty"`
	Platform     string   `json:"platform,omitempty"`
	Language     string   `json:"language,omitempty"`
	Build        string   `json:"build,omitempty"`
	RecentErrors []string `json:"recentErrors,omitempty"`
}

// IssueInput bundles everything buildIssueBody needs. Kept as a struct so
// adding fields doesn't churn every test signature.
type IssueInput struct {
	URL           string
	Comment       string
	OrgSlug       string
	UserEmail     string
	ServerVersion string
	GitHash       string
	FrontendBuild string
	Context       ContextPayload
	FileUID       string
	SignedURL     string
	SignedURLExp  time.Time
	MimeType      string
	ReportedAt    time.Time
}

// BuildIssueTitle returns the issue title: "Bug report: <first 60 chars>".
// Falls back to the URL path if no comment was given.
func BuildIssueTitle(input *IssueInput) string {
	const maxLen = 60

	subject := strings.TrimSpace(input.Comment)
	if subject == "" {
		subject = input.URL
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
func BuildIssueBody(input *IssueInput) string {
	var builder strings.Builder

	builder.WriteString("## Report\n\n")
	if input.Comment != "" {
		builder.WriteString(input.Comment)
	} else {
		builder.WriteString("_(no description)_")
	}

	builder.WriteString("\n\n## Page\n\n")
	builder.WriteString(input.URL)

	builder.WriteString("\n\n## Context\n\n")
	builder.WriteString("| Field | Value |\n|-------|-------|\n")
	writeContextRow(&builder, "Organization", input.OrgSlug)
	writeContextRow(&builder, "User", input.UserEmail)
	writeContextRow(&builder, "Server Version", joinNonEmpty(input.ServerVersion, input.GitHash, " "))
	writeContextRow(&builder, "Frontend Version", input.FrontendBuild)
	writeContextRow(&builder, "Browser", input.Context.UserAgent)
	writeContextRow(&builder, "Viewport", input.Context.Viewport)

	if input.Context.PixelRatio > 0 {
		writeContextRow(&builder, "Pixel Ratio", fmt.Sprintf("%g", input.Context.PixelRatio))
	}

	writeContextRow(&builder, "Platform", input.Context.Platform)
	writeContextRow(&builder, "Language", input.Context.Language)
	writeContextRow(&builder, "Reported At", input.ReportedAt.UTC().Format(time.RFC3339))

	if len(input.Context.RecentErrors) > 0 {
		builder.WriteString("\n## Console Errors\n\n```\n")

		for _, line := range input.Context.RecentErrors {
			builder.WriteString(strings.ReplaceAll(line, "```", "ʼʼʼ"))
			builder.WriteString("\n")
		}

		builder.WriteString("```\n")
	}

	if input.SignedURL != "" {
		mediaSection(&builder, input)
	}

	builder.WriteString("\n---\n*Auto-generated from in-app bug report*\n")

	return builder.String()
}

// mediaSection adds the Screenshot/Recording block. For images we inline; for
// videos we drop the bare URL so GitHub's auto-player picks it up, plus a
// download fallback link.
func mediaSection(builder *strings.Builder, input *IssueInput) {
	if strings.HasPrefix(input.MimeType, "video/") {
		builder.WriteString("\n## Screen recording\n\n")
		builder.WriteString(input.SignedURL)
		builder.WriteString("\n\n[Download](")
		builder.WriteString(input.SignedURL)
		builder.WriteString(")\n")
	} else {
		builder.WriteString("\n## Screenshot\n\n![Screenshot](")
		builder.WriteString(input.SignedURL)
		builder.WriteString(")\n")
	}

	fmt.Fprintf(builder,
		"\n<sub>File UID: `%s` · link valid until %s</sub>\n",
		input.FileUID,
		input.SignedURLExp.UTC().Format(time.RFC3339),
	)
}

func writeContextRow(builder *strings.Builder, key, value string) {
	if value == "" {
		value = "_unknown_"
	}

	builder.WriteString("| ")
	builder.WriteString(key)
	builder.WriteString(" | ")
	builder.WriteString(value)
	builder.WriteString(" |\n")
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
