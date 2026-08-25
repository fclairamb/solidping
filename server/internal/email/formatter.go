package email

import (
	"bytes"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"reflect"
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

// productLogoPath is the SolidPing logo served on every host, embedded from
// res/logo.png via `make sync-brand-assets`. PNG rather than SVG on purpose:
// SVG support in mail clients is patchy to non-existent.
const productLogoPath = "/dash0/logo.png"

// TemplateFormatter implements the Formatter interface using Go templates.
type TemplateFormatter struct {
	funcMap template.FuncMap
	// baseURL resolves the server base URL — the same one DashboardURL /
	// DocsURL are built from. Absolute asset URLs (the logo in base.html's
	// header) are derived from it. nil, or a func returning "", is legal: the
	// templates then fall back to their text wordmark instead of rendering a
	// broken image.
	baseURL func() string
}

// Option configures a TemplateFormatter. Variadic rather than a new required
// parameter so the many test constructions of a bare formatter keep compiling.
type Option func(*TemplateFormatter)

// WithBaseURL pins a fixed server base URL. Convenient for tests; production
// wiring wants WithBaseURLFunc — see why there.
func WithBaseURL(baseURL string) Option {
	return WithBaseURLFunc(func() string { return baseURL })
}

// WithBaseURLFunc sets a LATE-BOUND server base URL, read on every render.
//
// Late binding is not a nicety here. config.Server.BaseURL is not final when
// the formatter is constructed: NewServer builds it during wiring, and the
// systemconfig overlay (which is what actually applies SP_BASE_URL and the
// DB-stored `server.base_url` parameter) runs afterwards, in
// InitializeSystemConfig. A value captured at construction is therefore the
// pre-overlay default — every email would carry a localhost logo URL in
// production. Closing over the *config.Config the overlay mutates fixes that.
func WithBaseURLFunc(resolve func() string) Option {
	return func(f *TemplateFormatter) {
		f.baseURL = resolve
	}
}

// base returns the configured base URL with any trailing slash removed.
func (f *TemplateFormatter) base() string {
	if f.baseURL == nil {
		return ""
	}

	return strings.TrimRight(f.baseURL(), "/")
}

// NewFormatter creates a new template formatter.
func NewFormatter(opts ...Option) (*TemplateFormatter, error) {
	formatter := &TemplateFormatter{}

	for _, opt := range opts {
		opt(formatter)
	}

	formatter.funcMap = template.FuncMap{
		"upper": strings.ToUpper,
		"lower": strings.ToLower,
		"dict":  dict,
		"field": field,
		// absURL and productLogoURL close over the formatter so templates never
		// have to know the base URL — no view model carries it.
		"absURL":         formatter.absoluteURL,
		"productLogoURL": formatter.productLogoURL,
	}

	return formatter, nil
}

// productLogoURL returns the absolute URL of the SolidPing logo, or "" when no
// base URL is configured (in which case base.html renders its text wordmark).
func (f *TemplateFormatter) productLogoURL() string {
	base := f.base()
	if base == "" {
		return ""
	}

	return base + productLogoPath
}

// absoluteURL turns a stored logo reference into something an email client can
// actually load. Organization and status-page logos are stored either as an
// external "https://…" URL or as a site-relative "/pub/assets/<uid>" path, and
// a relative path in an email is simply a broken image — mail clients have no
// origin to resolve it against.
//
// Anything that is neither absolute-http(s) nor site-relative returns "",
// which the templates render as "no logo" rather than as a broken image. That
// also means a `javascript:` or `data:` value stored by a hostile admin can
// never reach an <img src>.
func (f *TemplateFormatter) absoluteURL(value any) string {
	raw := strings.TrimSpace(stringify(value))
	if raw == "" {
		return ""
	}

	if strings.HasPrefix(raw, "https://") || strings.HasPrefix(raw, "http://") {
		return raw
	}

	base := f.base()
	if !strings.HasPrefix(raw, "/") || base == "" {
		return ""
	}

	return base + raw
}

// stringify renders the handful of shapes a view-model value can arrive as
// (string, *string, nil) as a plain string. Anything else yields "".
func stringify(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case *string:
		if typed == nil {
			return ""
		}

		return *typed
	case fmt.Stringer:
		return typed.String()
	default:
		return ""
	}
}

// field looks a key up on a view model that may be either a map or a struct,
// returning nil when it is absent.
//
// This exists to dodge the trap documented in supportreply.go: html/template
// ERRORS on a missing struct field (while a missing map key is merely nil), and
// the repo's view models are a mix of map[string]any and structs
// (uptimereport.Data). A new `{{.OrgLogoURL}}` in base.html would therefore
// break every struct-backed template until the field was added to each one —
// silently, at send time. Every branding key base.html reads goes through this
// helper instead, so a view model that does not carry it renders empty.
func field(data any, name string) any {
	if data == nil {
		return nil
	}

	value := reflect.ValueOf(data)
	for value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}

		value = value.Elem()
	}

	// An if-chain rather than a switch on reflect.Kind: only two of the ~27
	// kinds are meaningful here, and enumerating the rest to satisfy an
	// exhaustiveness check would be noise.
	if value.Kind() == reflect.Map {
		if value.Type().Key().Kind() != reflect.String {
			return nil
		}

		found := value.MapIndex(reflect.ValueOf(name).Convert(value.Type().Key()))
		if !found.IsValid() {
			return nil
		}

		return found.Interface()
	}

	if value.Kind() == reflect.Struct {
		found := value.FieldByName(name)
		if !found.IsValid() || !found.CanInterface() {
			return nil
		}

		return found.Interface()
	}

	return nil
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

	result := make(map[string]any, len(args)/2) // half the arg count

	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			return nil, fmt.Errorf("%w: got %T", errDictKeyNotString, args[i])
		}

		result[key] = args[i+1]
	}

	return result, nil
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
