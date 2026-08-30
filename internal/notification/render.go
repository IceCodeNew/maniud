package notification

import (
	"path"
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
	title, supported, _ := notificationEventTitle(event.Kind)
	if !supported || !validGitOpsSourceEvent(event) {
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
		{name: "source", value: event.Source},
		{name: "reason", value: string(event.Reason)},
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

// SupportsEvent reports whether an application event can produce an external
// notification.
func SupportsEvent(kind application.EventKind) bool {
	_, supported, _ := notificationEventTitle(kind)

	return supported
}

func ignoredApplicationEvent(kind application.EventKind) bool {
	_, _, ignored := notificationEventTitle(kind)

	return ignored
}

func notificationEventTitle(kind application.EventKind) (string, bool, bool) {
	switch kind {
	case application.EventPlanPrepared:
		return planPreparedNotificationTitle, true, false
	case application.EventRuntimeEffectStarted:
		return "maniud runtime change started", true, false
	case application.EventTransactionSucceeded:
		return "maniud transaction succeeded", true, false
	case application.EventTransactionRestored:
		return "maniud automatic recovery succeeded", true, false
	case application.EventTransactionFailed:
		return "maniud transaction failed", true, false
	case application.EventGitOpsServiceApplyFailed:
		return "maniud GitOps service apply failed", true, false
	case application.EventGitOpsSourceBlocked:
		return "maniud GitOps source blocked", true, false
	case application.EventGitOpsSourceRecovered:
		return "maniud GitOps source recovered", true, false
	case application.EventDaemonUnavailable:
		return "maniud daemon unavailable", true, false
	case application.EventActionIntentRecorded,
		application.EventPostconditionObserved,
		application.EventActionCompleted,
		application.EventTransactionDegraded:
		return "", false, true
	default:
		return "", false, false
	}
}

func validGitOpsSourceEvent(event application.Event) bool {
	if event.Kind != application.EventGitOpsSourceBlocked &&
		event.Kind != application.EventGitOpsSourceRecovered {
		return event.Source == "" && event.Reason == ""
	}

	switch event.Reason {
	case application.EventReasonInvalidComposeSource:
		return validGitOpsNotificationSource(event.Source)
	case application.EventReasonRecoverySourceBlocked:
		return event.Source == ""
	default:
		return false
	}
}

func validGitOpsNotificationSource(source string) bool {
	if !validNotificationField(source) || path.Clean(source) != source ||
		path.Dir(source) != "services" {
		return false
	}

	return strings.HasSuffix(source, ".yaml") || strings.HasSuffix(source, ".yml")
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
