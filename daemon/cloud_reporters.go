package main

// cloudReporters lists every holder of the machine token that sends to the
// cloud on its own: push and reminders (Pusher), schedule and workflow
// notices, optional features' notices (cloudPusherHooks), ARIA,
// voice/feedback/usage reads (apiServer), crash reports, and update-outcome
// and fleet-status reports (Updater). Disconnect
// clears them all at once. Crash and update reports used to keep their copy
// of the token until the daemon restarted, so they kept sending after
// Disconnect.
//
// The background loops (usage scanners, fleet-status ticker, rotation poller,
// pusher poll loop) also stop when their context is cancelled; clearing the
// token here covers a send that is already past its context check.
type cloudReporters struct {
	api     *apiServer
	updater *Updater
	crash   *crashSink
	scheds  *ScheduleStore
}

// clear drops the machine token from every reporter and disables pusher, the
// live push channel. Safe to call repeatedly and with nothing wired.
func (r cloudReporters) clear(pusher *Pusher) {
	if pusher != nil {
		pusher.setToken("")
	}
	if r.api != nil {
		r.api.clearCloudCreds()
		if r.api.aria != nil {
			r.api.aria.setCloudCreds("", "")
		}
	}
	if r.updater != nil {
		r.updater.SetCloudCreds("", "")
	}
	if r.crash != nil {
		r.crash.setCreds("", "")
	}
	// Optional features' copies (extensions.go).
	runCloudPusherHooks(r.api, nil)
	if r.scheds != nil {
		r.scheds.SetPusher(nil)
	}
}
