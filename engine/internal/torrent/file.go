package torrent

import (
	"bytes"
	"errors"
	"fmt"

	"github.com/anacrolix/torrent/metainfo"
)

// ParseFile reads a .torrent file into what a State keeps of it: a magnet link
// (name, trackers and web seeds) and the bencoded info dict.
func ParseFile(b []byte) (magnet string, info []byte, err error) {
	mi, err := metainfo.Load(bytes.NewReader(b))
	if err != nil {
		return "", nil, fmt.Errorf("not a torrent file: %w", err)
	}
	inf, err := mi.UnmarshalInfo()
	if err != nil {
		return "", nil, fmt.Errorf("not a torrent file: %w", err)
	}
	if inf.PieceLength <= 0 || len(inf.Pieces) == 0 || inf.TotalLength() <= 0 {
		return "", nil, errors.New("not a torrent file: it describes no data")
	}
	m, err := mi.MagnetV2()
	if err != nil {
		return "", nil, fmt.Errorf("not a torrent file: %w", err)
	}
	return m.String(), mi.InfoBytes, nil
}
