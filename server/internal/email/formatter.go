package email

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"strings"

	"github.com/vanng822/go-premailer/premailer"
)

//go:embed templates/*
var templateFS embed.FS

// Errors returned by the "dict" template helper (see dict below).
var (
	errOddDictArgs      = errors.New("dict: odd number of arguments")
	errDictKeyNotString = errors.New("dict: key is not a string")
)

// TemplateFormatter implements the Formatter interface using Go templates.
type TemplateFormatter struct {
	funcMap template.FuncMap
}

// NewFormatter creates a new template formatter.
func NewFormatter() (*TemplateFormatter, error) {
	funcMap := template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"dict":  dict,
	}

	return &TemplateFormatter{
		funcMap: funcMap,
	}, nil
}

// dict builds a map[string]any from an alternating key/value argument list,
// e.g. {{template "button" dict "URL" .AckURL "Label" "Acknowledge"}}. Used
// to parameterize the shared "button" partial in base.html — html/template
// has no map literal syntax, so this is the standard workaround. Odd-length
// argument lists (a programmer error, not user input) return an error rather
// than silently dropping the trailing key.
func dict(args ...any) (map[string]any, error) {
	if len(args)%2 != 0 {
		return nil, errOddDictArgs
	}

	m := make(map[string]any, len(args)/2) //nolint:mnd // half the arg count, not a magic number

	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			return nil, fmt.Errorf("%w: got %T", errDictKeyNotString, args[i])
		}

		m[key] = args[i+1]
	}

	return m, nil
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
// none), the HTML body with inlined CSS, and a plaintext alternative (from a
// {{define "text"}} block, or "" when the template has none). See the
// Formatter interface for why there is no automatic HTML-to-text fallback.
func (f *TemplateFormatter) Format(templateName string, data any) (string, string, string, error) {
	tmpl, err := f.parseTemplate(templateName)
	if err != nil {
		return "", "", "", fmt.Errorf("parsing template %s: %w", templateName, err)
	}

	subject, err := f.renderSubject(tmpl, templateName, data)
	if err != nil {
		return "", "", "", err
	}

	text, err := f.renderText(tmpl, templateName, data)
	if err != nil {
		return "", "", "", err
	}

	var buf bytes.Buffer
	if execErr := tmpl.ExecuteTemplate(&buf, templateName, data); execErr != nil {
		return "", "", "", fmt.Errorf("executing template %s: %w", templateName, execErr)
	}

	prem, err := premailer.NewPremailerFromString(buf.String(), premailer.NewOptions())
	if err != nil {
		return "", "", "", fmt.Errorf("creating premailer: %w", err)
	}

	inlinedHTML, err := prem.Transform()
	if err != nil {
		return "", "", "", fmt.Errorf("inlining CSS: %w", err)
	}

	return subject, inlinedHTML, text, nil
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

// renderText executes a template's {{define "text"}} block, if present.
// Returns "" when no text block is defined — callers then send HTML-only,
// exactly as before this block existed.
func (f *TemplateFormatter) renderText(
	tmpl *template.Template, templateName string, data any,
) (string, error) {
	textTmpl := tmpl.Lookup("text")
	if textTmpl == nil {
		return "", nil
	}

	var buf bytes.Buffer
	if err := textTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("executing text for %s: %w", templateName, err)
	}

	return strings.TrimSpace(buf.String()), nil
}
