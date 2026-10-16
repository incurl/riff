// Package ed25519verify wraps crypto/ed25519 with the
// canonicalisation rules the PO-33 binding flow needs.
//
// The PO-33 device signs a binding authorization with an
// Ed25519 key whose public counterpart is encoded as a
// 32-byte raw (a.k.a. "seed" + "key" 32-byte form) blob. The
// signing input is the canonical JSON serialisation of an
// authorization request (sorted keys, no whitespace, UTF-8,
// fixed number rendering), and the signature is 64 raw bytes
// (the standard Ed25519 wire shape; no ASN.1 wrapping, no
// base64).
//
// The package is intentionally tiny and self-contained:
// it depends only on the standard library and the project
// internal/random helper. Callers feed in the public key,
// the request payload, and the signature; Verify returns
// nil on success and a typed sentinel on failure so the
// handler can return a precise Connect error code (e.g.
// Unauthenticated for a bad signature, InvalidArgument
// for a malformed key).
package ed25519verify
