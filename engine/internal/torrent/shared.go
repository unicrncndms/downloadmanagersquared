package torrent

import (
	"context"
	"sync"

	"github.com/anacrolix/torrent/metainfo"
	"github.com/anacrolix/torrent/storage"
)

// sharedStorage gives every line's client of one torrent the same TorrentImpl,
// so there is one set of file handles and one piece completion however many
// clients write. Two TorrentImpls over the same files break on Windows: the one
// finishing the file renames its .part while the other holds it open. The
// TorrentImpl closes when the last client has closed its torrent.
type sharedStorage struct {
	storage.ClientImpl
	mu   sync.Mutex
	open map[metainfo.Hash]*sharedTorrent
}

type sharedTorrent struct {
	impl storage.TorrentImpl
	refs int
}

func (s *sharedStorage) OpenTorrent(ctx context.Context, info *metainfo.Info, ih metainfo.Hash) (storage.TorrentImpl, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.open == nil {
		s.open = map[metainfo.Hash]*sharedTorrent{}
	}
	st := s.open[ih]
	if st == nil {
		impl, err := s.ClientImpl.OpenTorrent(ctx, info, ih)
		if err != nil {
			return impl, err
		}
		st = &sharedTorrent{impl: impl}
		s.open[ih] = st
	}
	st.refs++
	out := st.impl
	var once sync.Once
	out.Close = func() (err error) {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			if st.refs--; st.refs > 0 {
				return
			}
			if s.open[ih] == st {
				delete(s.open, ih)
			}
			err = st.impl.Close()
		})
		return err
	}
	return out, nil
}
