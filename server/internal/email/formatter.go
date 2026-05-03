package email

import (
	"bytes"
	"embed"
	"fmt"
	"html/template"
	"strings"

	"github.com/vanng822/go-premailer/premailer"
)

//go:embed templates/*
var templateFS embed.FS

// TemplateFormatter implements the Formatter interface using Go templates.
type TemplateFormatter struct {
	funcMap template.FuncMap
}

// NewFormatter creates a new template formatter.
func NewFormatter() (*TemplateFormatter, error) {
	funcMap := template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
	}

	return &TemplateFormatter{
		funcMap: funcMap,
	}, nil
}

// parseTemplate parses a specific template with the base template.
func (f *TemplateFormatter) parseTemplate(templateName string) (*template.Template, error) {
	// Read base template
	baseContent, err := templateFS.ReadFile("templates/base.html")
	if err != nil {
		return nil, fmt.Errorf("reading base template: %w", err)
	}

	// Read the specific template
	templateContent, err := templateFS.ReadFile("templates/" + templateName)
	if err != nil {
		return nil, fmt.Errorf("reading template %s: %w", templateName, err)
	}

	// Parse base template first
	tmpl, err := template.New("base.html").Funcs(f.funcMap).Parse(string(baseContent))
	if err != nil {
		return nil, fmt.Errorf("parsing base template: %w", err)
	}

	// Parse the specific template which defines the content block
	tmpl, err = tmpl.New(templateName).Parse(string(templateContent))
	if err != nil {
		return nil, fmt.Errorf("parsing template %s: %w", templateName, err)
	}

	return tmpl, nil
}

// Format renders a template with the given data and returns the rendered
// subject (from a {{define "subject"}} block, or "" when the template has
// none) and the HTML body with inlined CSS. See the Formatter interface
// for why no plaintext fallback is produced.
func (f *TemplateFormatter) Format(templateName string, data any) (string, string, error) {
	tmpl, err := f.parseTemplate(templateName)
	if err != nil {
		return "", "", fmt.Errorf("parsing template %s: %w", templateName, err)
	}

	subject, err := f.renderSubject(tmpl, templateName, data)
	if err != nil {
		return "", "", err
	}

	var buf bytes.Buffer
	if execErr := tmpl.ExecuteTemplate(&buf, templateName, data); execErr != nil {
		return "", "", fmt.Errorf("executing template %s: %w", templateName, execErr)
	}

	prem, err := premailer.NewPremailerFromString(buf.String(), premailer.NewOptions())
	if err != nil {
		return "", "", fmt.Errorf("creating premailer: %w", err)
	}

	inlinedHTML, err := prem.Transform()
	if err != nil {
		return "", "", fmt.Errorf("inlining CSS: %w", err)
	}

	return subject, inlinedHTML, nil
}

// renderSubject executes a template's {{define "subject"}} block, if present.
// Returns "" when no subject block is defined — callers may then fall back
// to a static subject.
func (f *TemplateFormatter) renderSubject(
	tmpl *template.Template, templateName string, data any,
) (string, error) {
	subjTmpl := tmpl.Lookup("subject")
	if subjTmpl == nil {
		return "", nil
	}

	var buf bytes.Buffer
	if err := subjTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing subject for %s: %w", templateName, err)
	}

	return strings.TrimSpace(buf.String()), nil
}
