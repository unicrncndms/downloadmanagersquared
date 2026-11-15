package torrent

import (
	"fmt"
	"testing"

	atorrent "github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// BenchmarkWanted walks a 10k-file torrent with some files skipped. The client
// listens on loopback only and never gets a peer.
func BenchmarkWanted(b *testing.B) {
	const files, fileLen, pieceLen = 10000, 1024, 16 << 10
	info := metainfo.Info{Name: "big", PieceLength: pieceLen}
	for i := range files {
		info.Files = append(info.Files, metainfo.FileInfo{Length: fileLen, Path: []string{fmt.Sprintf("f%d", i)}})
	}
	info.Pieces = make([]byte, 20*((files*fileLen+pieceLen-1)/pieceLen))
	ib, err := bencode.Marshal(info)
	if err != nil {
		b.Fatal(err)
	}
	cfg := atorrent.NewDefaultClientConfig()
	cfg.SetListenAddr("127.0.0.1:0")
	cfg.NoDHT, cfg.DisableUTP, cfg.DisableIPv6 = true, true, true
	cfg.NoDefaultPortForwarding, cfg.DisableWebtorrent = true, true
	stor := storage.NewFileOpts(storage.NewFileClientOpts{ClientBaseDir: b.TempDir(), PieceCompletion: storage.NewMapPieceCompletion()})
	cfg.DefaultStorage = stor
	cl, err := atorrent.NewClient(cfg)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cl.Close(); stor.Close() })
	at, err := cl.AddTorrent(&metainfo.MetaInfo{InfoBytes: ib})
	if err != nil {
		b.Fatal(err)
	}
	skip := []int{3, 500, 9999}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		wanted(at, skip)
	}
}
