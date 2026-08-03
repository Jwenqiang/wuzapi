package main

import "github.com/rs/zerolog/log"

func qrLifecycleEventName(event string) string {
	switch event {
	case "code":
		return "code_generated"
	case "success":
		return "channel_success"
	case "timeout":
		return "timeout"
	case "err-client-outdated":
		return "client_outdated"
	case "err-scanned-without-multidevice":
		return "scanned_without_multidevice"
	case "error":
		return "channel_error"
	case "err-unexpected-state":
		return "unexpected_state"
	default:
		return ""
	}
}

func logQRLifecycleEvent(event string) {
	log.Info().Str("qr_event", event).Msg("WhatsApp QR lifecycle")
}
