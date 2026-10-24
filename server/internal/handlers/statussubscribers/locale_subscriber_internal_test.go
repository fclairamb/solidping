package statussubscribers

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/email"
	"github.com/fclairamb/solidping/server/internal/i18n"
)

// TestLabelInEnglishIsTheEnglishLabel pins the English catalog to the banner
// every update kind showed before the mail was localized (label()).
func TestLabelInEnglishIsTheEnglishLabel(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	for _, kind := range []MailKind{MailKindIncidentOpened, MailKindUpdate, MailKindResolved} {
		r.Equal(kind.label(), kind.LabelIn(i18n.English), kind)
	}

	r.Equal("Nouvel incident", MailKindIncidentOpened.LabelIn(i18n.New("fr", "")), "positive control")
}

// TestSubjectsInEnglishAreTheEnglishSubjects pins the subjects the templates
// now render in English to the ones MailKind.subject built in Go: a page name
// and a title only, and "Confirm your subscription to <page>".
func TestSubjectsInEnglishAreTheEnglishSubjects(t *testing.T) {
	t.Parallel()
	r := require.New(t)

	formatter, err := email.NewFormatter()
	r.NoError(err)

	data := &MailData{PageName: "Search & Discovery", Title: "API <down>", ConfirmURL: "https://x.test/c"}

	subject, _, _, err := formatter.Format("status-subscriber-confirm.html", i18n.English,
		map[string]any{"PageName": data.PageName, "ConfirmURL": data.ConfirmURL})
	r.NoError(err)
	r.Equal(MailKindConfirm.subject(data), subject)

	for _, kind := range []MailKind{MailKindIncidentOpened, MailKindUpdate, MailKindResolved} {
		subject, _, _, err = formatter.Format("status-subscriber-update.html", i18n.English, map[string]any{
			"Label": kind.LabelIn(i18n.English), "Title": data.Title, "PageName": data.PageName,
			"BodyMarkdown": "", "LinkURL": "", "SubscriberUnsubscribeURL": "https://x.test/u",
		})
		r.NoError(err)
		r.Equal(kind.subject(data), subject, kind)
	}
}
