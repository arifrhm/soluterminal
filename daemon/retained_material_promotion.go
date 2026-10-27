package main

import (
	"errors"

	"path/filepath"
	"strings"
)

func (a *retirementAuthorityStore) validatePromotion(m retirementManifest, p retirementPromotion) error {
	if !previewDigestRE.MatchString(p.Digest) || !p.Identity.Valid {
		return errors.New("retirement promotion digest/identity invalid")
	}
	expected := filepath.Join(filepath.Dir(a.root), "retirement-material", "preview", strings.TrimPrefix(p.Digest, "sha256:"))
	if p.Destination != expected || p.Source == p.Destination {
		return errors.New("retirement promotion destination invalid")
	}
	for _, e := range m.Entries {
		for _, pin := range e.Pins {
			if pin.Path == p.Source && pin.Kind == "preview_bundle" && pin.Digest == p.Digest && pin.Identity == p.Identity && !pin.Missing {
				return nil
			}
		}
	}
	return errors.New("retirement promotion is outside prepared scope")
}

func (a *retirementAuthorityStore) reconcilePromotion(p retirementPromotion) error {
	return nil
}
