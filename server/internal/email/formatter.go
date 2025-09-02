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

// Format renders a template with the given data.
// Returns both HTML (with inlined CSS) and plain text versions.
func (f *TemplateFormatter) Format(templateName string, data any) (string, string, error) {
	var buf bytes.Buffer

	// Parse the template with base
	tmpl, err := f.parseTemplate(templateName)
	if err != nil {
		return "", "", fmt.Errorf("parsing template %s: %w", templateName, err)
	}

	// Execute the child template which invokes base.html
	if execErr := tmpl.ExecuteTemplate(&buf, templateName, data); execErr != nil {
		return "", "", fmt.Errorf("executing template %s: %w", templateName, execErr)
	}

	html := buf.String()

	// Inline CSS using premailer
	prem, err := premailer.NewPremailerFromString(html, premailer.NewOptions())
	if err != nil {
		return "", "", fmt.Errorf("creating premailer: %w", err)
	}

	inlinedHTML, err := prem.Transform()
	if err != nil {
		return "", "", fmt.Errorf("inlining CSS: %w", err)
	}

	// Generate plain text version
	plainText, err := prem.TransformText()
	if err != nil {
		return "", "", fmt.Errorf("generating plain text: %w", err)
	}

	return inlinedHTML, plainText, nil
}
