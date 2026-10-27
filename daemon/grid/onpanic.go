package grid

// OnPanic, when set by the host package, is invoked with a site label and the
// recovered value whenever the emulator recovers a panic — so the daemon can
// ship a redacted crash report to the cloud. nil by default, which keeps this
// package dependency-free (it must not import the daemon's main package).
var OnPanic func(site string, recovered any)
