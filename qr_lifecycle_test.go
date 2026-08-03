package main

import "testing"

func TestQRLifecycleEventName(t *testing.T) {
	tests := []struct {
		name  string
		event string
		want  string
	}{
		{name: "code generated", event: "code", want: "code_generated"},
		{name: "channel success", event: "success", want: "channel_success"},
		{name: "timeout", event: "timeout", want: "timeout"},
		{name: "client outdated", event: "err-client-outdated", want: "client_outdated"},
		{name: "scanned without multidevice", event: "err-scanned-without-multidevice", want: "scanned_without_multidevice"},
		{name: "channel error", event: "error", want: "channel_error"},
		{name: "unexpected state", event: "err-unexpected-state", want: "unexpected_state"},
		{name: "unknown event omitted", event: "other", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := qrLifecycleEventName(tt.event); got != tt.want {
				t.Fatalf("qrLifecycleEventName(%q) = %q; want %q", tt.event, got, tt.want)
			}
		})
	}
}
