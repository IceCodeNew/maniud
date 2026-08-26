package notification

import (
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	notificationTruncationMarker  = "…"
	planPreparedNotificationTitle = "maniud plan prepared"
)

type notificationMessage struct {
	title string
	body  string
}

func renderApplicationEvent(event application.Event) (notificationMessage, bool) {
	title, supported := notificationEventTitle(event.Kind)
	if !supported {
		return notificationMessage{}, false
	}

	fields := []string{"event: " + string(event.Kind)}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "plan", value: string(event.Plan)},
		{name: "project", value: event.Project},
		{name: "service", value: event.Service},
		{name: "runtime", value: string(event.Runtime)},
		{name: "transaction", value: event.Transaction},
		{name: "action", value: event.Action},
		{name: "evidence", value: event.Evidence},
	} {
		if field.value == "" {
			continue
		}
		if !validNotificationField(field.value) {
			return notificationMessage{}, false
		}
		fields = append(fields, field.name+": "+field.value)
	}
	if event.Sequence > 0 {
		fields = append(fields, "sequence: "+strconv.FormatInt(event.Sequence, 10))
	}
	if event.Satisfied {
		fields = append(fields, "satisfied: true")
	}

	body := truncateNotificationText(strings.Join(fields, "\n"), maximumNotificationBody)

	return notificationMessage{title: title, body: body}, true
}

func notificationEventTitle(kind application.EventKind) (string, bool) {
	switch kind {
	case application.EventPlanPrepared:
		return planPreparedNotificationTitle, true
	case application.EventRuntimeEffectStarted:
		return "maniud runtime change started", true
	case application.EventTransactionSucceeded:
		return "maniud transaction succeeded", true
	case application.EventTransactionRestored:
		return "maniud automatic recovery succeeded", true
	case application.EventTransactionFailed:
		return "maniud transaction failed", true
	case application.EventGitOpsServiceApplyFailed:
		return "maniud GitOps service apply failed", true
	case application.EventDaemonUnavailable:
		return "maniud daemon unavailable", true
	case application.EventActionIntentRecorded,
		application.EventPostconditionObserved,
		application.EventActionCompleted,
		application.EventTransactionDegraded:
		return "", false
	default:
		return "", false
	}
}

func validNotificationField(value string) bool {
	if !utf8.ValidString(value) || strings.ContainsRune(value, 0) {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}

	return true
}

func truncateNotificationText(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}

	end := maximum - len(notificationTruncationMarker)
	for end > 0 && !utf8.RuneStart(value[end]) {
		end--
	}

	return value[:end] + notificationTruncationMarker
}
