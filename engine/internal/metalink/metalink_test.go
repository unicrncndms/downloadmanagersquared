package metalink

import (
	"slices"
	"strings"
	"testing"
)

const sample = `<?xml version="1.0" encoding="UTF-8"?>
<metalink xmlns="urn:ietf:params:xml:ns:metalink">
  <published>2026-01-02T03:04:05Z</published>
  <file name="ubuntu.iso">
    <size>14471447</size>
    <hash type="md5">D41D8CD98F00B204E9800998ECF8427E</hash>
    <hash type="sha-256">e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855</hash>
    <hash type="sha-1">not used</hash>
    <url location="de">https://b.example/ubuntu.iso</url>
    <url location="us" priority="1">https://a.example/ubuntu.iso</url>
    <url priority="2">ftp://f.example/ubuntu.iso</url>
    <metaurl mediatype="torrent">https://a.example/ubuntu.iso.torrent</metaurl>
  </file>
  <file name="only-ftp.txt">
    <url>ftp://f.example/only-ftp.txt</url>
  </file>
  <file name="notes.txt">
    <url>http://c.example/notes.txt</url>
  </file>
</metalink>`

func TestParseReadsFilesHashesAndMirrorsInPriorityOrder(t *testing.T) {
	fs, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	if len(fs) != 2 {
		t.Fatalf("%d files, want 2 (the ftp-only one left out): %+v", len(fs), fs)
	}
	f := fs[0]
	if f.Name != "ubuntu.iso" || f.Size != 14471447 || f.MD5 != "d41d8cd98f00b204e9800998ecf8427e" ||
		f.SHA256 != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("file %+v", f)
	}
	if want := []string{"https://a.example/ubuntu.iso", "https://b.example/ubuntu.iso"}; !slices.Equal(f.URLs, want) {
		t.Fatalf("mirrors %q, want %q", f.URLs, want)
	}
	if fs[1].Name != "notes.txt" || fs[1].Size != 0 || fs[1].SHA256 != "" {
		t.Fatalf("second file %+v", fs[1])
	}
}

func TestParseRefusesWhatIsNotAMetalink4(t *testing.T) {
	for _, b := range []string{
		"<html>no</html>",
		`<metalink version="3.0" xmlns="http://www.metalinker.org/"><files/></metalink>`,
		`<metalink xmlns="urn:ietf:params:xml:ns:metalink"><file name="x"><url>ftp://a/x</url></file></metalink>`,
	} {
		if _, err := Parse([]byte(b)); err == nil {
			t.Errorf("%s: no error", strings.SplitN(b, ">", 2)[0])
		}
	}
}
