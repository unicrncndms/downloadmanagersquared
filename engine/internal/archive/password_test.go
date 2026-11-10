package archive

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The password archives hold Game/readme.txt of solid.7z, packed with the
// password "secret" (see sevenzip_test.go and rar_test.go).
var passwordArchives = []string{"password.7z", "password-header.7z", "password.rar", "password-header.rar", "rar4-header.rar"}

// openWith reads a whole archive with a password and extracts all of it. err
// is the first ErrWrongPassword, from Read or from Extract.
func openWith(t *testing.T, name, password, out string) (Result, error) {
	t.Helper()
	d := newDownload(t, testdata7z(t, name))
	d.arrive(0, int64(len(d.data)))
	a, err := ReadWith(d.f, int64(len(d.data)), password)
	if err != nil {
		return Result{}, err
	}
	x := extractor(t, a, d.f, out)
	defer x.Close()
	var all Result
	for {
		r, err := x.Extract(context.Background(), d.missing)
		all.Done, all.Freed, all.Failed = append(all.Done, r.Done...), append(all.Freed, r.Freed...), append(all.Failed, r.Failed...)
		if err != nil || !r.More {
			return all, err
		}
	}
}

func TestAnEncryptedArchiveAsksForItsPassword(t *testing.T) {
	for _, name := range passwordArchives {
		b := testdata7z(t, name)
		if _, err := ReadWith(bytes.NewReader(b), int64(len(b)), ""); !errors.Is(err, ErrPassword) {
			t.Fatalf("%s without a password: %v", name, err)
		}
	}
}

func TestAWrongPasswordIsRefusedAndNothingIsFreedOrFailed(t *testing.T) {
	for _, name := range passwordArchives {
		r, err := openWith(t, name, "wrong", t.TempDir())
		if !errors.Is(err, ErrWrongPassword) {
			t.Fatalf("%s: %v, want the wrong password", name, err)
		}
		if len(r.Done)+len(r.Freed)+len(r.Failed) > 0 {
			t.Fatalf("%s: done %v, freed %v, failed %v", name, r.Done, r.Freed, r.Failed)
		}
	}
}

func TestTheRightPasswordExtractsIt(t *testing.T) {
	want := want7z(t, testdata7z(t, "solid.7z"))
	for _, name := range passwordArchives {
		out := t.TempDir()
		r, err := openWith(t, name, "secret", out)
		if err != nil || len(r.Failed) > 0 || len(r.Done) == 0 {
			t.Fatalf("%s: %v, failed %v", name, err, r.Failed)
		}
		rel := "readme.txt" // a 7z's Root, Game, is the output folder
		if strings.HasSuffix(name, ".rar") {
			rel = filepath.Join("Game", rel)
		}
		if got, err := os.ReadFile(filepath.Join(out, rel)); err != nil || !bytes.Equal(got, want["Game/readme.txt"]) {
			t.Fatalf("%s: readme.txt wrong (%v)", name, err)
		}
	}
}
