package winutil

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Range is the bytes [Start, End) of a file.
type Range struct{ Start, End int64 }

// FreeRange gives the disk space of n bytes at off back, on a file SetSparse
// marked sparse: they read as zeros from then on, and the file keeps its
// size. NTFS frees whole allocation units (64 KB on most disks), so an
// unaligned edge is only zeroed and stays allocated.
func FreeRange(f *os.File, off, n int64) error {
	if n <= 0 {
		return nil
	}
	in := struct{ start, end int64 }{off, off + n} // FILE_ZERO_DATA_INFORMATION
	var ret uint32
	return windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_SET_ZERO_DATA,
		(*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)), nil, 0, &ret, nil)
}

// AllocatedRanges is where a sparse file has disk space, in order. Anything
// else reads as zeros.
func AllocatedRanges(f *os.File) ([]Range, error) {
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	type allocated struct{ off, n int64 } // FILE_ALLOCATED_RANGE_BUFFER
	var out []Range
	buf := make([]allocated, 128)
	for start, size := int64(0), fi.Size(); start < size; {
		in := allocated{start, size - start}
		var ret uint32
		err := windows.DeviceIoControl(windows.Handle(f.Fd()), windows.FSCTL_QUERY_ALLOCATED_RANGES,
			(*byte)(unsafe.Pointer(&in)), uint32(unsafe.Sizeof(in)),
			(*byte)(unsafe.Pointer(&buf[0])), uint32(len(buf))*uint32(unsafe.Sizeof(buf[0])), &ret, nil)
		more := errors.Is(err, windows.ERROR_MORE_DATA)
		if err != nil && !more {
			return nil, err
		}
		got := buf[:ret/uint32(unsafe.Sizeof(buf[0]))]
		for _, a := range got {
			out = append(out, Range{a.off, a.off + a.n})
		}
		if !more || len(got) == 0 {
			break
		}
		start = got[len(got)-1].off + got[len(got)-1].n
	}
	return out, nil
}
