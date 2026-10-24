package emailpreview_test

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/fclairamb/solidping/server/internal/handlers/emailpreview"
)

// subscriberPreview is what one language must show in the two
// status-subscriber previews.
type subscriberPreview struct {
	confirmSubject, confirmBody, confirmText, confirmEnglish string
	updateSubject, updateBody, updateText, updateEnglish     string
}

// subscriberPreviews holds, per language, a translated subject, an HTML
// sentence, a text-part line, and an English sentence that must be gone.
//
//nolint:gochecknoglobals // read-only expectations table.
var subscriberPreviews = map[string]subscriberPreview{
	"en": {
		confirmSubject: "Confirm your subscription to Acme Status",
		confirmBody:    "You asked to receive status updates for <strong>Acme Status</strong>.",
		confirmText:    "You asked to receive status updates. Confirm your email address:",
		updateSubject:  "Acme Status: Elevated error rates",
		updateBody:     "You&#39;re receiving this because you subscribed to Acme Status.",
		updateText:     "More information: https://solidping.example/s/acme/acme-status",
	},
	"fr": {
		confirmSubject: "Confirmez votre abonnement à Acme Status",
		confirmBody:    "Vous avez demandé à recevoir les mises à jour concernant <strong>Acme Status</strong>.",
		confirmText:    "Confirmez votre adresse e-mail :",
		confirmEnglish: "You asked to receive",
		updateSubject:  "Acme Status : Taux d'erreur élevé",
		updateBody:     "Vous recevez cet e-mail dans le cadre de votre abonnement à Acme Status.",
		updateText:     "Plus d'informations : https://solidping.example/s/acme/acme-status",
		updateEnglish:  "You&#39;re receiving this",
	},
	"de": {
		confirmSubject: "Bestätigen Sie Ihr Abonnement für Acme Status",
		confirmBody:    "Sie haben Status-Updates zu <strong>Acme Status</strong> angefordert.",
		confirmText:    "Bestätigen Sie Ihre E-Mail-Adresse:",
		confirmEnglish: "You asked to receive",
		updateSubject:  "Acme Status: Erhöhte Fehlerraten",
		updateBody:     "Sie erhalten diese E-Mail, weil Sie Acme Status abonniert haben.",
		updateText:     "Weitere Informationen: https://solidping.example/s/acme/acme-status",
		updateEnglish:  "You&#39;re receiving this",
	},
	"es": {
		confirmSubject: "Confirma tu suscripción a Acme Status",
		confirmBody:    "Has pedido recibir las actualizaciones de <strong>Acme Status</strong>.",
		confirmText:    "Confirma tu dirección de correo:",
		confirmEnglish: "You asked to receive",
		updateSubject:  "Acme Status: Tasas de error elevadas",
		updateBody:     "Recibes este correo porque te suscribiste a Acme Status.",
		updateText:     "Más información: https://solidping.example/s/acme/acme-status",
		updateEnglish:  "You&#39;re receiving this",
	},
}

// TestPreview_SubscriberTemplatesInEveryLocale renders both status-subscriber
// previews in each language: index subject, HTML and text part.
func TestPreview_SubscriberTemplatesInEveryLocale(t *testing.T) {
	t.Parallel()

	router := newTestRouter(t)

	for lang, want := range subscriberPreviews {
		t.Run(lang, func(t *testing.T) {
			t.Parallel()
			r := require.New(t)

			index := doGet(t, router, "/api/mgmt/email-preview?locale="+lang)
			r.Equal(http.StatusOK, index.Code)

			var payload struct {
				Data []emailpreview.TemplateSummary `json:"data"`
			}
			r.NoError(json.Unmarshal(index.Body.Bytes(), &payload))

			subjects := map[string]string{}
			for _, row := range payload.Data {
				subjects[row.Template] = row.Subject
			}

			r.Equal(want.confirmSubject, subjects["status-subscriber-confirm.html"])
			r.Equal(want.updateSubject, subjects["status-subscriber-update.html"])

			base := "/api/mgmt/email-preview/status-subscriber-"

			confirm := doGet(t, router, base+"confirm.html?locale="+lang).Body.String()
			r.Contains(confirm, want.confirmBody)

			confirmText := doGet(t, router, base+"confirm.html?format=text&locale="+lang).Body.String()
			r.Contains(confirmText, want.confirmSubject+"\n")
			r.Contains(confirmText, want.confirmText)

			update := doGet(t, router, base+"update.html?locale="+lang).Body.String()
			r.Contains(update, want.updateBody)

			updateText := doGet(t, router, base+"update.html?format=text&locale="+lang).Body.String()
			r.Contains(updateText, want.updateText)

			if lang != "en" {
				r.NotContains(confirm, want.confirmEnglish)
				r.NotContains(update, want.updateEnglish)
				r.Contains(subscriberPreviews["en"].updateBody, want.updateEnglish, "the English sentence the check looks for")
			}
		})
	}
}
