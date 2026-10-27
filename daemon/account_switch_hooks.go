package main

// The private account-switch feature installs its notifier wiring at initialization.
// A public build has no account switchers and leaves this hook inert.
var bindAccountSwitchNotifiers = func(_ *uiBus, _ *apiServer, _, _ any) {}
