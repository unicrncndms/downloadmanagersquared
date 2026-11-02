package winutil

import (
	"bytes"
	"testing"
)

func TestProtectedDataComesBackOnlyUnprotected(t *testing.T) {
	secret := []byte(`{"user":"p0123456","pass":"hunter2"}`)
	sealed, err := Protect(secret)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte("hunter2")) {
		t.Fatal("the password is readable in the protected data")
	}
	got, err := Unprotect(sealed)
	if err != nil || !bytes.Equal(got, secret) {
		t.Fatalf("unprotected %q, %v", got, err)
	}
	if _, err := Unprotect([]byte("not protected")); err == nil {
		t.Fatal("garbage unprotected")
	}
}
