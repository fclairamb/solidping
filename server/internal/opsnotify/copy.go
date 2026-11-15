package opsnotify

import (
	"strings"

	"github.com/fclairamb/solidping/server/internal/i18n"
)

// Subject tags. They read the same in every language: an operator's mail
// filter matches on them.
const (
	tagInstance = "[SolidPing] "
	tagSupport  = "[SolidPing support] "
)

// Localizer writes a notice for one reader: the subject, then the body, in
// their language, with dates in their timezone. Only the email medium calls it
// (spec 2026-10-21-01, decision 11): every other medium sends the notice's
// Subject and Body, the English text, byte for byte as before.
type Localizer interface {
	Localize(loc i18n.Locale) (string, string)
}

// SupportMessage is a support.message notice in structured form. It travels
// the job queue (jobtypes.OperatorNoticeJobConfig), so its JSON tags are a
// compatibility contract with queued rows.
type SupportMessage struct {
	// NewThread says the message opened a thread.
	NewThread bool `json:"newThread,omitempty"`
	// Channel is the bot channel it came in on (sms, telegram...).
	Channel string `json:"channel"`
	// From is the sender's identity on that channel.
	From string `json:"from"`
	// Preview is the start of the message, as the stranger typed it.
	Preview string `json:"preview"`
	// Folded is how many messages of the thread were swallowed since the
	// last notice.
	Folded int `json:"folded,omitempty"`
}

// NewSupportMessageNotice is the notice for an inbound support message. url
// opens the thread.
func NewSupportMessageNotice(msg *SupportMessage, url string) *Notice {
	subject, body := msg.Localize(i18n.English)

	return &Notice{Event: EventSupportMessage, Subject: subject, Body: body, URL: url, Localized: msg}
}

// Localize implements Localizer. Plain text only: the preview is typed by a
// stranger, and every renderer downstream escapes it, so nothing here may
// look like markup.
func (m *SupportMessage) Localize(loc i18n.Locale) (string, string) {
	subjectKey := "notices.supportMessage.subjectNewMessage"
	if m.NewThread {
		subjectKey = "notices.supportMessage.subjectNewThread"
	}

	lines := []string{
		loc.T("notices.supportMessage.channel", "channel", m.Channel),
		loc.T("notices.supportMessage.from", "from", m.From),
		"",
		m.Preview,
	}

	if m.Folded > 0 {
		lines = append(lines, "", loc.Tn("notices.supportMessage.folded", m.Folded))
	}

	lines = append(lines, "", loc.T("notices.supportMessage.replyWarning"))

	return tagSupport + loc.T(subjectKey, "channel", m.Channel, "from", m.From), strings.Join(lines, "\n")
}

// UserRegistered is a user.registered notice in structured form. It travels
// the job queue like SupportMessage.
type UserRegistered struct {
	Email string `json:"email"`
	// Name is the display name, often empty.
	Name string `json:"name,omitempty"`
	// Method is the signup method (password, google, invite...).
	Method string `json:"method"`
	// Orgs are the organizations the user landed in, known only at delivery
	// time (see SetLandingOrgs). NoOrg says they joined none. Neither set:
	// the notice has no organization line.
	Orgs  []string `json:"orgs,omitempty"`
	NoOrg bool     `json:"noOrg,omitempty"`
}

// NewUserRegisteredNotice is the notice for a new account. userUID is the new
// user, whose landing organization the delivery resolves.
func NewUserRegisteredNotice(user *UserRegistered, userUID string) *Notice {
	subject, body := user.Localize(i18n.English)

	return &Notice{
		Event:        EventUserRegistered,
		Subject:      subject,
		Body:         body,
		AboutUserUID: userUID,
		Localized:    user,
	}
}

// Localize implements Localizer.
func (u *UserRegistered) Localize(loc i18n.Locale) (string, string) {
	body := loc.T("notices.userRegistered.intro") + "\n\n" +
		loc.T("notices.userRegistered.email", "email", u.Email) + "\n"

	if u.Name != "" {
		body += loc.T("notices.userRegistered.name", "name", u.Name) + "\n"
	}

	method := methodLabel(loc, u.Method)
	body += loc.T("notices.userRegistered.method", "method", method)

	if len(u.Orgs) > 0 || u.NoOrg {
		body += "\n" + orgLine(loc, u.Orgs)
	}

	return tagInstance + loc.T("notices.userRegistered.subject", "email", u.Email, "method", method), body
}

// methodLabel names a signup method. The two plain words (password, invite)
// are translated; the provider names (google, saml, ldap...) read the same in
// every language. The English labels are the method values themselves.
func methodLabel(loc i18n.Locale, method string) string {
	switch method {
	case "password":
		return loc.T("notices.userRegistered.methods.password")
	case "invite":
		return loc.T("notices.userRegistered.methods.invite")
	default:
		return method
	}
}

// SetLandingOrgs records the organizations the new user of a user.registered
// notice landed in, none meaning they joined no organization. It runs at
// delivery time (see AboutUserUID), on the English Body every medium sends and
// on the structured form the email is written from.
func (n *Notice) SetLandingOrgs(slugs []string) {
	n.Body += "\n" + orgLine(i18n.English, slugs)

	if registered, ok := n.Localized.(*UserRegistered); ok {
		registered.Orgs = slugs
		registered.NoOrg = len(slugs) == 0
	}
}

// orgLine lists the landing organizations. The label agrees with the list
// (one organization or several), which is not a count-based plural: the line
// shows names, never a number.
func orgLine(loc i18n.Locale, slugs []string) string {
	switch len(slugs) {
	case 0:
		return loc.T("notices.userRegistered.noOrg")
	case 1:
		return loc.T("notices.userRegistered.org", "orgs", slugs[0])
	default:
		return loc.T("notices.userRegistered.orgs", "orgs", strings.Join(slugs, ", "))
	}
}

// operatorTest is the "Send me a test" notice in structured form.
type operatorTest struct {
	requestedBy string
}

// NewTestNotice is the notice the "Send me a test" button delivers.
// requestedBy is the address of the operator who pressed it, url the
// dashboard.
func NewTestNotice(requestedBy, url string) *Notice {
	test := &operatorTest{requestedBy: requestedBy}
	subject, body := test.Localize(i18n.English)

	return &Notice{Event: EventTest, Subject: subject, Body: body, URL: url, Localized: test}
}

// Localize implements Localizer.
func (t *operatorTest) Localize(loc i18n.Locale) (string, string) {
	return tagInstance + loc.T("notices.test.subject"),
		loc.T("notices.test.intro") + "\n" + loc.T("notices.test.works") + "\n\n" +
			loc.T("notices.test.requestedBy", "email", t.requestedBy)
}

// SMSDestinationProbing is an sms.destination_probing notice in structured
// form. It travels the job queue like SupportMessage, so its JSON tags are a
// compatibility contract with queued rows.
type SMSDestinationProbing struct {
	// OrgUID is the flagged organization.
	OrgUID string `json:"orgUid"`
	// OrgSlug is its slug, empty when it could not be resolved.
	OrgSlug string `json:"orgSlug,omitempty"`
	// Refusals is how many allow-list refusals flagged it.
	Refusals int `json:"refusals"`
	// WindowMinutes is the window those refusals happened in.
	WindowMinutes int `json:"windowMinutes"`
	// Prefixes are the refused destination prefixes (leading digits only,
	// never a full number).
	Prefixes []string `json:"prefixes,omitempty"`
	// BlockMinutes is how long the org's verification sends stay refused.
	BlockMinutes int `json:"blockMinutes"`
}

// NewSMSDestinationProbingNotice is the notice for an organization flagged for
// SMS destination probing.
func NewSMSDestinationProbingNotice(probe *SMSDestinationProbing) *Notice {
	subject, body := probe.Localize(i18n.English)

	return &Notice{Event: EventSMSDestinationProbing, Subject: subject, Body: body, Localized: probe}
}

// Localize implements Localizer.
func (p *SMSDestinationProbing) Localize(loc i18n.Locale) (string, string) {
	org := p.OrgUID
	if p.OrgSlug != "" {
		org = p.OrgSlug + " (" + p.OrgUID + ")"
	}

	prefixes := make([]string, 0, len(p.Prefixes))

	for _, prefix := range p.Prefixes {
		if prefix == "" {
			prefixes = append(prefixes, "?")

			continue
		}

		prefixes = append(prefixes, "+"+prefix)
	}

	lines := []string{
		loc.Tn("notices.smsDestinationProbing.intro", p.Refusals, "minutes", p.WindowMinutes),
		"",
		loc.T("notices.smsDestinationProbing.org", "org", org),
		loc.T("notices.smsDestinationProbing.prefixes", "prefixes", strings.Join(prefixes, ", ")),
		"",
		loc.T("notices.smsDestinationProbing.blocked", "minutes", p.BlockMinutes),
	}

	return tagInstance + loc.T("notices.smsDestinationProbing.subject", "org", org), strings.Join(lines, "\n")
}
