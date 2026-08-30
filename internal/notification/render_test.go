package notification

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const notificationTestSource = "services/api.yaml"

func TestRenderApplicationEventProjectsSupportedNotifications(t *testing.T) {
	t.Parallel()

	event := application.Event{
		Plan:        application.PlanUpgrade,
		Project:     "example",
		Service:     "api",
		Runtime:     domain.RuntimeDocker,
		Transaction: "transaction",
		Action:      "replace_workload",
		Sequence:    2,
		Evidence:    "sha256:evidence",
		Satisfied:   true,
	}
	tests := []struct {
		kind  application.EventKind
		title string
	}{
		{kind: application.EventPlanPrepared, title: planPreparedNotificationTitle},
		{kind: application.EventRuntimeEffectStarted, title: "maniud runtime change started"},
		{kind: application.EventTransactionSucceeded, title: "maniud transaction succeeded"},
		{kind: application.EventTransactionRestored, title: "maniud automatic recovery succeeded"},
		{kind: application.EventTransactionFailed, title: "maniud transaction failed"},
		{kind: application.EventGitOpsServiceApplyFailed, title: "maniud GitOps service apply failed"},
		{kind: application.EventDaemonUnavailable, title: "maniud daemon unavailable"},
	}
	wantBody := "event: %s\nplan: upgrade\nproject: example\nservice: api\nruntime: docker\n" +
		"transaction: transaction\naction: replace_workload\nevidence: sha256:evidence\nsequence: 2\nsatisfied: true"

	for _, test := range tests {
		t.Run(string(test.kind), func(t *testing.T) {
			t.Parallel()

			current := event
			current.Kind = test.kind
			message, valid := renderApplicationEvent(current)
			if !valid || message.title != test.title || message.body != strings.Replace(wantBody, "%s", string(test.kind), 1) {
				t.Fatalf("renderApplicationEvent() = %#v, %t", message, valid)
			}
		})
	}
}

func TestRenderApplicationEventOmitsEmptyAndDefaultFields(t *testing.T) {
	t.Parallel()

	message, valid := renderApplicationEvent(application.Event{Kind: application.EventPlanPrepared})
	if !valid || message != (notificationMessage{
		title: planPreparedNotificationTitle,
		body:  "event: plan_prepared",
	}) {
		t.Fatalf("renderApplicationEvent(minimal) = %#v, %t", message, valid)
	}
}

func TestRenderApplicationEventProjectsGitOpsSourceTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		event application.Event
		want  notificationMessage
	}{
		{
			name: "blocked source",
			event: application.Event{
				Kind: application.EventGitOpsSourceBlocked, Source: notificationTestSource,
				Reason: application.EventReasonInvalidComposeSource,
			},
			want: notificationMessage{
				title: "maniud GitOps source blocked",
				body: "event: gitops_source_blocked\nsource: services/api.yaml\n" +
					"reason: invalid_compose_source",
			},
		},
		{
			name: "recovery source recovered",
			event: application.Event{
				Kind:   application.EventGitOpsSourceRecovered,
				Reason: application.EventReasonRecoverySourceBlocked,
			},
			want: notificationMessage{
				title: "maniud GitOps source recovered",
				body:  "event: gitops_source_recovered\nreason: recovery_source_blocked",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, valid := renderApplicationEvent(test.event)
			if !valid || got != test.want {
				t.Fatalf("renderApplicationEvent() = %#v, %t", got, valid)
			}
		})
	}
}

func TestRenderApplicationEventRejectsUnsupportedOrInvalidValues(t *testing.T) {
	t.Parallel()

	for _, event := range []application.Event{
		{},
		{Kind: application.EventActionIntentRecorded},
		{Kind: application.EventPostconditionObserved},
		{Kind: application.EventActionCompleted},
		{Kind: application.EventTransactionDegraded},
		{Kind: application.EventPlanPrepared, Project: "bad\nproject"},
		{Kind: application.EventPlanPrepared, Service: "bad\x00service"},
		{Kind: application.EventPlanPrepared, Action: string([]byte{0xff})},
		{
			Kind: application.EventPlanPrepared, Source: notificationTestSource,
			Reason: application.EventReasonInvalidComposeSource,
		},
		{Kind: application.EventGitOpsSourceBlocked},
		{
			Kind: application.EventGitOpsSourceBlocked, Source: "/private/source.yaml",
			Reason: application.EventReasonInvalidComposeSource,
		},
		{
			Kind: application.EventGitOpsSourceBlocked, Source: "services/../source.yaml",
			Reason: application.EventReasonInvalidComposeSource,
		},
		{
			Kind: application.EventGitOpsSourceBlocked, Source: "services/source.txt",
			Reason: application.EventReasonInvalidComposeSource,
		},
		{
			Kind: application.EventGitOpsSourceRecovered, Source: notificationTestSource,
			Reason: application.EventReasonRecoverySourceBlocked,
		},
		{
			Kind:   application.EventGitOpsSourceRecovered,
			Reason: "unknown_reason",
		},
	} {
		if message, valid := renderApplicationEvent(event); valid || message != (notificationMessage{}) {
			t.Fatalf("renderApplicationEvent(%#v) = %#v, %t", event, message, valid)
		}
	}
}

func TestRenderApplicationEventTruncatesAtUTF8Boundary(t *testing.T) {
	t.Parallel()

	message, valid := renderApplicationEvent(application.Event{
		Kind:    application.EventPlanPrepared,
		Project: strings.Repeat("\u754c", maximumNotificationBody),
	})
	if !valid || len(message.body) > maximumNotificationBody || !utf8.ValidString(message.body) ||
		!strings.HasSuffix(message.body, notificationTruncationMarker) {
		t.Fatalf("truncated event body = %q, valid %t", message.body, valid)
	}

	unchanged := "already bounded"
	if got := truncateNotificationText(unchanged, len(unchanged)); got != unchanged {
		t.Fatalf("truncateNotificationText(bounded) = %q", got)
	}
	splitBoundary := strings.Repeat("x", 12) + "\u754c\u754c"
	if got := truncateNotificationText(splitBoundary, 16); got != strings.Repeat("x", 12)+notificationTruncationMarker {
		t.Fatalf("truncateNotificationText(split rune) = %q", got)
	}
}
