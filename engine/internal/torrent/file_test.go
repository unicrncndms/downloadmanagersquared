package torrent

import (
	"bytes"
	"strings"
	"testing"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
)

func TestTorrentFileKeepsItsTrackersWebSeedsAndInfo(t *testing.T) {
	sw := newSwarm(t, 0, 300<<10)
	mi := metainfo.MetaInfo{
		InfoBytes: sw.infoBytes(t),
		Announce:  "udp://tracker.example:1337/announce",
		UrlList:   []string{"https://mirror.example/file.bin"},
	}
	var buf bytes.Buffer
	if err := mi.Write(&buf); err != nil {
		t.Fatal(err)
	}
	magnet, info, err := ParseFile(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(info, mi.InfoBytes) {
		t.Fatal("the info dict changed")
	}
	for _, want := range []string{sw.hash.HexString(), "tracker.example", "mirror.example", "dn=file.bin"} {
		if !strings.Contains(magnet, want) {
			t.Fatalf("magnet %q lacks %q", magnet, want)
		}
	}
}

func TestNotATorrentFileIsRefused(t *testing.T) {
	notInfo, _ := bencode.Marshal(map[string]string{"hello": "world"})
	for _, b := range [][]byte{[]byte("<html>login first</html>"), notInfo} {
		if _, _, err := ParseFile(b); err == nil {
			t.Fatalf("%q was taken as a torrent file", b)
		}
	}
}
