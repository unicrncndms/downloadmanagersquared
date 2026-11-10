package download

import (
	"cmp"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"

	"downloadmanagersquared/engine/internal/winutil"
)

// SetHashes sets the checksums the file must have ("" for none). An
// unfinished download checks them when it finishes; a finished one checks its
// file now.
func (d *Download) SetHashes(sha, md string) error {
	// Not while Entries are being freed: what is freed decides whether a hash can apply.
	d.xmu.Lock()
	defer d.xmu.Unlock()
	sha, md = strings.ToLower(strings.TrimSpace(sha)), strings.ToLower(strings.TrimSpace(md))
	if err := checkHex(sha, sha256.Size, "SHA-256"); err != nil {
		return err
	}
	if err := checkHex(md, md5.Size, "MD5"); err != nil {
		return err
	}
	d.mu.Lock()
	if f := d.st.Folder; (sha != "" || md != "") && f != nil {
		if f.locked() {
			d.mu.Unlock()
			return errors.New("the archive is being extracted as it downloads, so there won't be a whole file to check")
		}
		if f.On {
			f.On, f.Note = false, hashNote
			d.folderOffLocked()
		}
	}
	d.st.SHA256, d.st.MD5, d.st.HashResult = sha, md, ""
	check := d.st.Status == StatusCompleted && (sha != "" || md != "") && !d.checking
	path := filepath.Join(d.st.Dir, d.st.Filename)
	if check {
		d.checking = true
	}
	d.mu.Unlock()
	d.env.Changed(d)
	if check {
		go d.checkFinished(path)
	}
	return nil
}

// checkHex: s is empty or n bytes in hex.
func checkHex(s string, n int, name string) error {
	if s == "" {
		return nil
	}
	if b, err := hex.DecodeString(s); err != nil || len(b) != n {
		return fmt.Errorf("a %s checksum is %d hex characters (0-9, a-f)", name, 2*n)
	}
	return nil
}

// checkFinished checks a finished file against the checksums, again if they
// changed meanwhile.
func (d *Download) checkFinished(path string) {
	for {
		d.mu.Lock()
		sha, md := d.st.SHA256, d.st.MD5
		d.mu.Unlock()
		res := "match"
		if sha != "" || md != "" {
			if err := verify(context.Background(), path, sha, md); err != nil {
				res = err.Error()
				if !errors.Is(err, errMismatch) {
					res = "could not check the file: " + err.Error()
				}
			}
		}
		d.mu.Lock()
		again := d.st.SHA256 != sha || d.st.MD5 != md
		if !again {
			d.checking = false
			if sha != "" || md != "" {
				d.st.HashResult = res
			}
		}
		d.mu.Unlock()
		d.env.Changed(d)
		if !again {
			return
		}
	}
}

func (d *Download) complete(ctx context.Context) error {
	d.mu.Lock()
	f := d.file
	d.file = nil
	sparse := d.st.RangeOK
	sha, md := d.st.SHA256, d.st.MD5
	fromServer := sha == "" && md == "" && d.st.Version.hashed() // a pasted checksum wins
	if fromServer {
		sha, md = d.st.Version.SHA256, d.st.Version.MD5
	}
	part := d.partPath()
	dir, name := d.st.Dir, d.st.Filename
	folder := d.folderOnLocked()
	set := d.set
	d.mu.Unlock()

	if _, _, ok := Splittable(name); (ok || set != nil) && folder {
		// A part of a split archive: its Set reads the .part, frees it and
		// deletes it once extracted.
		if f != nil {
			f.Close()
		}
		if set != nil && set.finished.Load() {
			os.Remove(part)
		}
		return nil
	}
	if folder {
		// Every Entry is extracted and checked: the archive itself isn't kept.
		if f != nil {
			f.Close()
		}
		if err := os.Remove(part); err != nil {
			log.Printf("download %s: removing the partial file: %v", d.st.ID, err)
		}
		if fromServer {
			d.mu.Lock()
			d.st.Folder.Note = serverMD5
			d.mu.Unlock()
		}
		return nil
	}
	if f != nil {
		if sparse {
			winutil.SetSparse(f, false)
		}
		f.Sync()
		f.Close()
	}
	if sha != "" || md != "" {
		d.mu.Lock()
		d.st.Status = StatusVerifying
		d.mu.Unlock()
		d.env.Changed(d)
		if err := verify(ctx, part, sha, md); err != nil {
			switch {
			case errors.Is(err, errMismatch) && fromServer:
				return fmt.Errorf("%s from the server %w: the file arrived damaged, remove the download and add it again", strings.Fields(err.Error())[0], errMismatch)
			case errors.Is(err, errMismatch):
				return fmt.Errorf("%w: remove the download and add it again, or check the value", err)
			}
			return err
		}
		d.mu.Lock()
		d.st.HashResult = "match"
		d.mu.Unlock()
	}
	d.markFunc()(part)
	final := filepath.Join(dir, uniqueName(dir, name, ""))
	if err := os.Rename(part, final); err != nil {
		return fmt.Errorf("could not rename finished file: %w", err)
	}
	d.mu.Lock()
	d.st.Filename = filepath.Base(final)
	d.mu.Unlock()
	return nil
}

// markFunc writes the Mark of the Web, as a browser would, on a file this
// download produced. A failure (a volume without alternate data streams) is
// only logged: the file is fine without it.
func (d *Download) markFunc() func(path string) {
	d.mu.Lock()
	id, host, ref := d.st.ID, d.st.URL, cmp.Or(d.st.Referer, d.st.PageURL)
	d.mu.Unlock()
	return func(path string) {
		if err := winutil.MarkFromInternet(path, host, ref); err != nil {
			log.Printf("download %s: marking %s as from the internet: %v", id, filepath.Base(path), err)
		}
	}
}

func verify(ctx context.Context, path, wantSHA, wantMD5 string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var hs []io.Writer
	var sh, mh hash.Hash
	if wantSHA != "" {
		sh = sha256.New()
		hs = append(hs, sh)
	}
	if wantMD5 != "" {
		mh = md5.New()
		hs = append(hs, mh)
	}
	if _, err := io.Copy(io.MultiWriter(hs...), ctxReader{ctx, f}); err != nil {
		return err
	}
	if sh != nil && hex.EncodeToString(sh.Sum(nil)) != wantSHA {
		return fmt.Errorf("SHA-256 %w", errMismatch)
	}
	if mh != nil && hex.EncodeToString(mh.Sum(nil)) != wantMD5 {
		return fmt.Errorf("MD5 %w", errMismatch)
	}
	return nil
}

var errMismatch = errors.New("does not match")

type ctxReader struct {
	ctx context.Context
	r   io.Reader
}

func (c ctxReader) Read(p []byte) (int, error) {
	if err := c.ctx.Err(); err != nil {
		return 0, err
	}
	return c.r.Read(p)
}
