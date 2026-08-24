package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"xf/controller/pkg/dblib"
	"xf/controller/pkg/flowweb"
	"xf/controller/pkg/plugauth"
)

const (
	scopeUser        = "koreader:user"
	collBooks   = "koreader_books"
)

// Book is this plug's owned record. Add the fields the product needs; every
// surface is generated from the resource.Spec in entity.go, so nothing else changes.
type Book struct {
	ID   string
	Name string

	Subject   string
	Workspace string
	CreatedAt string
}

func newID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func nowRFC() string { return time.Now().UTC().Format(time.RFC3339) }

// tenant maps a verified caller to its storage key, resolving a demo subject to its own
// ephemeral workspace exactly like every other plug. Isolation is a property of this
// function, which is why manifest_test.go asserts it directly.
func tenant(subject string) dblib.TenantKey {
	subject = strings.TrimSpace(subject)
	if t, err := flowweb.TenantForCaller(subject); err == nil && t.Valid() && t.Workspace != dblib.PersonalWorkspace(subject) {
		t.Visible = nil
		return t
	}
	return dblib.TenantKey{
		Account:   plugauth.CanonicalTenantID(subject),
		Workspace: dblib.PersonalWorkspace(subject),
		Subject:   subject,
	}
}
