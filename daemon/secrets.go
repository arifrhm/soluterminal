package main

import (
	"os"
	"strings"
)

// secretResolver resolves a runtime-scoped secret: the value of env (an
// environment variable name such as TYPESAFE_API_KEY) for a runtime ID.
type secretResolver interface {
	Resolve(runtimeID, env string) (value string, ok bool, err error)
}

// runtimeSecretStore opens this daemon's runtime secret store. nil in a build
// without one: secrets then come from the daemon's own environment.
var runtimeSecretStore func(dataDir string) secretResolver

// openRuntimeSecrets is the secret source core features (the Jev decision
// bridge, ARIA's smart approvals, the model gateway) resolve keys from.
func openRuntimeSecrets(dataDir string) secretResolver {
	if runtimeSecretStore != nil {
		return runtimeSecretStore(dataDir)
	}
	return envSecrets{}
}

// envSecrets resolves a secret from the daemon's environment.
type envSecrets struct{}

func (envSecrets) Resolve(_, env string) (string, bool, error) {
	value := strings.TrimSpace(os.Getenv(env))
	return value, value != "", nil
}

// The TypeSafe (Jev) smart-approval key.
const (
	jevTypesafeRuntimeID = "typesafe-api"
	jevTypesafeSecretEnv = "TYPESAFE_API_KEY"
)

// resourceOverrides are the shared resource settings, or the defaults when no
// settings store is built in.
func (a *apiServer) resourceOverrides() resourceSettings {
	if a == nil || a.resources == nil {
		return resourceSettings{}
	}
	return a.resources.resourceOverrides()
}
