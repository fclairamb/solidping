package discord

// Discord embed colors.
const (
	ColorRed    = 16711680 // #FF0000 - Active/reopened incidents
	ColorGreen  = 65280    // #00FF00 - Resolved incidents
	ColorOrange = 16744448 // #FFA500 - Escalations
	ColorBlue   = 3447003  // #3498DB - Info
)

// Embed represents a Discord embed object.
type Embed struct {
	Title       string  `json:"title,omitempty"`
	URL         string  `json:"url,omitempty"`
	Description string  `json:"description,omitempty"`
	Color       int     `json:"color,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
	Timestamp   string  `json:"timestamp,omitempty"`
	Footer      *Footer `json:"footer,omitempty"`
}

// Field represents a field in a Discord embed.
type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}

// Footer represents the footer of a Discord embed.
type Footer struct {
	Text string `json:"text"`
}
