package main

// device.go — a KOReader device's identity, and how it comes to have one.
//
// # WHY A PAIRING CODE AND NOT A PASSWORD
//
// The reader is an e-ink device with a stiff on-screen keyboard and a two-second
// refresh. Typing an email address and a password there is miserable, and it is the
// wrong shape besides: identity in this mesh is Zitadel, and this plug must never come
// to own a password.
//
// So the TV flow. A short code is minted on a real screen by somebody already signed in,
// typed once on the device, and exchanged for a long-lived per-device token. The device
// inherits an identity it never handled credentials for, and the code is worthless to
// anyone who did not just authenticate to mint it.
//
// # THE TOKEN IS STORED AS A HASH
//
// This fleet stores secret keys in plaintext where the plaintext is what drives the
// service — encryption at rest is obfuscation when the key must be replayed. THIS
// SECRET IS DIFFERENT: we issue it, and we never need it back. The device presents it,
// we hash and compare. So only the hash is stored, plus the last four characters for
// display, and a stolen database yields nothing that can sync.
//
// One token per device, revocable on its own. A lost Kobo costs one revoke rather than
// a password change.

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
)

const (
	collDevices   = "koreader_devices"
	collPairCodes = "koreader_paircodes"
	collTokens    = "koreader_tokens"

	// pairCodeTTL is how long a minted code is worth typing. Long enough to walk to
	// the sofa and wake a sleeping e-reader; short enough that a code left on a
	// screen is not a standing invitation.
	pairCodeTTL = 15 * time.Minute

	// pairCodeLen is 6 characters from an UNAMBIGUOUS alphabet. On e-ink at low
	// contrast the difference between 0/O and 1/I/l is a coin toss, and a person
	// mistyping a code they read correctly cannot tell which of the two went wrong.
	pairCodeLen      = 6
	pairCodeAlphabet = "ACDEFGHJKMNPQRTUVWXY34679"
)

// PairCode is a minted, unclaimed invitation. It carries the tenant that minted it, so
// claiming it is what binds a device to a person.
type PairCode struct {
	ID        string // the code itself, uppercased — the lookup key
	Subject   string
	Workspace string
	Account   string
	CreatedAt string
	ExpiresAt string
	ClaimedAt string
	DeviceID  string
}

// Device is one paired reader.
type Device struct {
	ID        string
	Name      string
	Subject   string
	Workspace string
	Account   string

	// TokenSHA256 is the hex digest of the bearer token. The token itself is shown
	// exactly once, when the device claims its code, and is never stored.
	TokenSHA256 string
	// TokenTail is the last four characters, for telling two devices apart on a page
	// without printing either one.
	TokenTail string

	CreatedAt  string
	LastSeenAt string
	LastSyncAt string
	Revoked    bool

	// What the device told us about itself on its first sync. Recorded because the
	// KOReader statistics schema moved between versions and the plan cannot be
	// finished until we know which one a real device writes — so the first sync's
	// job is to report its own environment.
	KOReaderVersion string
	StatsSchema     string
	DeviceModel     string

	// UTCOffset is the device's own offset from UTC, in seconds, as it reported on its
	// last sync. It is what decides WHICH DAY a page turn belongs to: the reader's sense
	// of "I read on Tuesday" comes from the clock on the e-reader in their hands, and on
	// an unconfigured Kobo that clock is UTC whatever zone they are standing in. A
	// server-side guess from the account's timezone gets this wrong by a day for anyone
	// who reads late — see readingDate.
	//
	// Zero means "not reported", which is also the value an unconfigured device sends,
	// and both resolve to UTC. That collision is deliberate: they want the same answer.
	UTCOffset int

	// CaptureReport is what the device last said about capturing bookmarked pages —
	// "12 attempted, 0 captured, 12 failed: <reason>". Surfaced on the Devices page,
	// because a failure that only exists in an e-reader's log does not exist.
	CaptureReport string
}

var errNoSuchCode = errors.New("that code is not valid — check it, or mint a new one")

// newPairCode mints an unclaimed code for a tenant.
func newPairCode(tk dblib.TenantKey) (PairCode, error) {
	code, err := randomCode()
	if err != nil {
		return PairCode{}, err
	}
	now := time.Now().UTC()
	return PairCode{
		ID: code, Subject: tk.Subject, Workspace: tk.Workspace, Account: tk.Account,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(pairCodeTTL).Format(time.RFC3339),
	}, nil
}

// randomCode draws from crypto/rand with REJECTION SAMPLING rather than a modulo.
// The alphabet is 25 characters and 256 is not a multiple of it, so `b % 25` would make
// the first six letters measurably likelier — a small bias, but the kind that is free to
// avoid and embarrassing to explain.
func randomCode() (string, error) {
	const max = byte(256 - (256 % len(pairCodeAlphabet)))
	out := make([]byte, 0, pairCodeLen)
	buf := make([]byte, 1)
	for len(out) < pairCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		if buf[0] >= max {
			continue
		}
		out = append(out, pairCodeAlphabet[int(buf[0])%len(pairCodeAlphabet)])
	}
	return string(out), nil
}

// normalizeCode is what a person typed, made comparable. Uppercased, and stripped of the
// spaces and dashes a reader inserts because the code is printed in groups.
func normalizeCode(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	return strings.NewReplacer(" ", "", "-", "", "_", "").Replace(s)
}

// expired reports whether a code is past its window. A missing or unparseable ExpiresAt
// counts as EXPIRED: a code whose expiry cannot be read is a code with no expiry, and
// failing open on that would make every malformed record a permanent invitation.
func (p PairCode) expired(now time.Time) bool {
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(p.ExpiresAt))
	if err != nil {
		return true
	}
	return now.After(t)
}

// mintToken returns the plaintext token and its stored form. 32 bytes of crypto/rand.
func mintToken() (plain string, sha string, tail string, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", "", "", err
	}
	plain = "kor_" + base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(plain))
	sha = hex.EncodeToString(sum[:])
	tail = plain[len(plain)-4:]
	return plain, sha, tail, nil
}

// hashToken is the comparison form of a presented token.
func hashToken(plain string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(plain)))
	return hex.EncodeToString(sum[:])
}

// claimPairCode exchanges a typed code for a device and its one-time token.
//
// SINGLE USE, and the claim is written BEFORE the token is returned. A code that could be
// claimed twice would hand two devices the same person's identity, and the window for
// that is exactly as long as the gap between the check and the write.
func (a *app) claimPairCode(ctx context.Context, typed, deviceName string) (Device, string, error) {
	code := normalizeCode(typed)
	if len(code) != pairCodeLen {
		return Device{}, "", errNoSuchCode
	}
	// Pair codes are looked up OUTSIDE any tenant: the whole point is that the device
	// does not yet know whose it is. The code's own record carries the tenant.
	pc, err := a.pairCodes.Get(ctx, pairCodeTenant(), code)
	if err != nil || pc == nil {
		return Device{}, "", errNoSuchCode
	}
	if strings.TrimSpace(pc.ClaimedAt) != "" {
		return Device{}, "", errors.New("that code has already been used — mint a new one")
	}
	if pc.expired(time.Now().UTC()) {
		return Device{}, "", errors.New("that code has expired — mint a new one")
	}

	plain, sha, tail, err := mintToken()
	if err != nil {
		return Device{}, "", err
	}
	name := strings.TrimSpace(deviceName)
	if name == "" {
		name = "KOReader"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	dev := Device{
		ID: newID(), Name: name,
		Subject: pc.Subject, Workspace: pc.Workspace, Account: pc.Account,
		TokenSHA256: sha, TokenTail: tail,
		CreatedAt: nowRFC(),
	}
	tk := dblib.TenantKey{Subject: pc.Subject, Workspace: pc.Workspace, Account: pc.Account}
	if err := a.devices.Put(ctx, tk, dev.ID, &dev); err != nil {
		return Device{}, "", err
	}
	ref := TokenRef{ID: sha, DeviceID: dev.ID, Subject: pc.Subject, Workspace: pc.Workspace, Account: pc.Account}
	if err := a.tokens.Put(ctx, pairCodeTenant(), ref.ID, &ref); err != nil {
		return Device{}, "", err
	}
	pc.ClaimedAt, pc.DeviceID = nowRFC(), dev.ID
	if err := a.pairCodes.Put(ctx, pairCodeTenant(), pc.ID, pc); err != nil {
		// The device exists and the code is still open. Refuse the token rather than
		// hand one out against a code that can be claimed again — the caller retries
		// and the orphaned device row is harmless.
		return Device{}, "", fmt.Errorf("could not close the pairing code: %w", err)
	}
	return dev, plain, nil
}

// TokenRef points a token's HASH at the device that holds it. It lives in the shared
// pairing space, because resolving a token is what establishes the tenant — looking it up
// inside a tenant would require already knowing the answer.
//
// KEYED BY THE HASH, NEVER THE TOKEN. A hash is not a credential, so it is safe in a key
// space that reaches logs and error messages; the token itself appears in no record at
// all. And it is a single Get rather than a scan, so resolving a device costs the same
// whether one person has paired a reader or ten thousand have.
type TokenRef struct {
	ID        string // sha256 of the token, hex
	DeviceID  string
	Subject   string
	Workspace string
	Account   string
	Revoked   bool
}

// deviceForToken resolves a presented bearer token to its device and tenant.
func (a *app) deviceForToken(ctx context.Context, token string) (Device, dblib.TenantKey, bool) {
	token = strings.TrimSpace(token)
	// Shape-checked before it is hashed. A missing header and a malformed one are the
	// overwhelming majority of bad tokens, and neither deserves a database round trip.
	if !strings.HasPrefix(token, "kor_") || len(token) < 20 {
		return Device{}, dblib.TenantKey{}, false
	}
	ref, err := a.tokens.Get(ctx, pairCodeTenant(), hashToken(token))
	if err != nil || ref == nil || ref.Revoked {
		return Device{}, dblib.TenantKey{}, false
	}
	tk := dblib.TenantKey{Subject: ref.Subject, Workspace: ref.Workspace, Account: ref.Account}
	dev, err := a.devices.Get(ctx, tk, ref.DeviceID)
	if err != nil || dev == nil || dev.Revoked {
		return Device{}, dblib.TenantKey{}, false
	}
	// The digest matched a record keyed by that digest, so this compare is a
	// belt-and-braces check that the two rows agree. Constant-time because it costs
	// nothing and because a timing signal on a token comparison is the kind of thing
	// that is obvious only after somebody exploits it.
	if subtle.ConstantTimeCompare([]byte(dev.TokenSHA256), []byte(ref.ID)) != 1 {
		return Device{}, dblib.TenantKey{}, false
	}
	return *dev, tk, true
}

// pairCodeTenant is the shared space unclaimed codes live in. They are pre-identity by
// construction — a device presenting a code has no tenant yet — so they cannot be stored
// under the minter's key without the device already knowing who that is.
func pairCodeTenant() dblib.TenantKey {
	return dblib.TenantKey{Subject: "pairing", Account: "pairing", Workspace: "pairing"}
}
