package main

import (
	"log"
	"sync"
)

var unknownEventMu sync.Mutex
var unknownEvents = map[string]bool{}

func warnUnknownEvent(scope, typ string) {
	key := scope + "\x00" + typ
	unknownEventMu.Lock()
	first := !unknownEvents[key]
	unknownEvents[key] = true
	unknownEventMu.Unlock()
	if first {
		log.Printf("unknown %s event type %q skipped", scope, typ)
	}
}
