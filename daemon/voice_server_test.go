package main

// withVoice gives a test server its voice state.
func withVoice(a *apiServer, v *voiceServerState) *apiServer {
	voiceServerSlot.put(&a.ext, v)
	return a
}
