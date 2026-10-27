# Update verification

How the app checks its updates. It describes the verification boundary; it
does not certify a release artifact.

The updater checks binary SHA-256 values against an update manifest. Builds
with an embedded Ed25519 verification key also require a valid signature over
the manifest bytes. Older builds without that key use HTTPS and hashes; the
existence of signature-checking code does not prove an installed build enforces it.

Before distributing a release, establish the exact binary/source revision,
embedded key, signed manifest and negative signature-verification results.
Keep signing keys outside the repository and export. The private release
operator's publishing tools and key setup are not included in this repository.

A signed manifest does not by itself establish Windows publisher signing,
reproducible builds, equivalence to public source, relay end-to-end encryption
or an independent security assessment. State each assurance only when its own
artifact and verification evidence exist.
