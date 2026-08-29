package notifications

import "strings"

// unackEmoji is the product-wide identity of `incident.unacknowledged`. A
// warning sign, deliberately NOT a checkmark and deliberately not the green of
// a recovery: the incident is not over and, unlike a moment ago, nobody owns
// it. Change every side together (see ackEmoji).
const unackEmoji = "⚠️"

// fieldLabelUnacknowledgedBy labels the actor on card/attachment-style
// senders, so a retraction reads identically whichever channel it lands in.
const fieldLabelUnacknowledgedBy = "Withdrawn by"

// unackTitle is the one-line header a retraction carries, e.g.
// "⚠️ Acknowledgment withdrawn: api-health (#42)".
func unackTitle(payload *Payload) string {
	name := getCheckName(payload.Check)
	ref := strings.TrimSpace(incidentRefPrefix(payload.Incident))

	if ref != "" {
		return unackEmoji + " Acknowledgment withdrawn: " + name + " (" + ref + ")"
	}

	return unackEmoji + " Acknowledgment withdrawn: " + name
}

// unackHeadline is the actor half of the sentence: "Acknowledgment withdrawn
// by Alice via Slack". Reuses the ack attribution helpers, so the person who
// withdrew is resolved by exactly the code that resolves the person who acked.
func unackHeadline(payload *Payload) string {
	headline := "Acknowledgment withdrawn by " + ackActor(payload.Acknowledgment)

	if via := ackViaLabel(payload.Acknowledgment); via != "" {
		headline += " " + via
	}

	return headline
}

// unackCallToAction is the half of the message that MATTERS, and its exact
// claim is load-bearing.
//
// It must say that escalation resumes, because it does: unack reschedules the
// escalation cycle from the step the acknowledgment interrupted (decision
// 2026-08-28, option (c)). Wording that merely said "needs someone to take it"
// would leave a reader to assume paging had stopped — which was the OTHER
// option on the table and is not what shipped. Getting this wrong in either
// direction is worse than the silence it replaces: an operator who believes
// pages have stopped will camp on a chat message, and one who believes they
// have resumed when they have not will go back to bed.
const unackCallToAction = "This incident is unowned again — escalation resumes."

// unackPlainBody is the plain-text rendering for channels with no markup.
func unackPlainBody(payload *Payload) string {
	return unackHeadline(payload) + ". " + unackCallToAction
}

// unackSentence is unackPlainBody's headline without punctuation, for senders
// that compose it into a larger line.
func unackSentence(payload *Payload) string {
	return unackHeadline(payload)
}
