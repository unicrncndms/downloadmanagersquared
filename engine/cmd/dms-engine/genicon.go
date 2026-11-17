//go:build ignore

// Regenerate the exe resources: cd engine/cmd/dms-engine && go run genicon.go
// also writes extension/icons/icon-{16,32,48,128}.png (Chromium) and
// writes icon_windows_amd64.syso (RT_ICON/RT_GROUP_ICON and RT_VERSION) and
// ../dms-host/version_windows_amd64.syso (RT_VERSION); `go build` links them in.
// The version resource names the exe: Windows heads its notifications, Task
// Manager and Installed apps with FileDescription, else with the file name.
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"unicode/utf16"

	"downloadmanagersquared/engine/internal/mark"
)

func main() {
	sizes := []int{16, 20, 24, 32, 40, 48, 64, 256}
	imgs := make([][]byte, len(sizes))
	for i, n := range sizes {
		imgs[i] = mark.PNG(mark.Draw(n, nil))
	}

	// Chromium takes no SVG for manifest icons.
	for _, n := range []int{16, 32, 48, 128} {
		if err := os.WriteFile(fmt.Sprintf("../../../extension/icons/icon-%d.png", n), mark.PNG(mark.Draw(n, nil)), 0o644); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.WriteFile("../../../extension/icons/icon.svg", []byte(mark.SVG), 0o644); err != nil {
		log.Fatal(err)
	}

	// Resource tree: type -> name -> language -> data entry.
	// RT_ICON(3): ids 1..len(sizes); RT_GROUP_ICON(14): id 1.
	var group bytes.Buffer
	binary.Write(&group, binary.LittleEndian, []uint16{0, 1, uint16(len(sizes))})
	for i, n := range sizes {
		b := byte(n)
		if n >= 256 {
			b = 0
		}
		group.Write([]byte{b, b, 0, 0})
		binary.Write(&group, binary.LittleEndian, []uint16{1, 32})
		binary.Write(&group, binary.LittleEndian, uint32(len(imgs[i])))
		binary.Write(&group, binary.LittleEndian, uint16(i+1))
	}
	var leaves []leaf
	for i := range sizes {
		leaves = append(leaves, leaf{3, uint32(i + 1), imgs[i]})
	}
	leaves = append(leaves, leaf{14, 1, group.Bytes()})
	leaves = append(leaves, leaf{16, 1, versionInfo("DMS²", "dms-engine.exe")})
	writeSyso("icon_windows_amd64.syso", leaves)
	writeSyso("../dms-host/version_windows_amd64.syso", []leaf{{16, 1, versionInfo("DMS² browser connector", "dms-host.exe")}})
}

type leaf struct {
	typ, id uint32
	data    []byte
}

// versionInfo is a VS_VERSIONINFO naming the exe. Its numbers stay 0: the
// syso is checked in, and the version is set when building.
func versionInfo(desc, file string) []byte {
	le := binary.LittleEndian
	var fixed bytes.Buffer // VS_FIXEDFILEINFO
	binary.Write(&fixed, le, [13]uint32{0xFEEF04BD, 0x00010000, 0, 0, 0, 0, 0x3F, 0, 0x40004 /* VOS_NT_WINDOWS32 */, 1 /* VFT_APP */, 0, 0, 0})
	var strs [][]byte
	for _, kv := range [][2]string{
		{"CompanyName", "downloadmanagersquared"},
		{"FileDescription", desc},
		{"InternalName", file},
		{"LegalCopyright", "MIT licence"},
		{"OriginalFilename", file},
		{"ProductName", "DMS²"},
	} {
		v := utf16z(kv[1])
		strs = append(strs, verBlock(kv[0], v, 1, len(v)/2))
	}
	table := verBlock("040904B0", nil, 1, 0, strs...) // en-US, Unicode
	trans := verBlock("Translation", []byte{0x09, 0x04, 0xB0, 0x04}, 0, 4)
	return verBlock("VS_VERSION_INFO", fixed.Bytes(), 0, fixed.Len(),
		verBlock("StringFileInfo", nil, 1, 0, table),
		verBlock("VarFileInfo", nil, 1, 0, trans))
}

// verBlock is one block of a version resource: its length, the length of its
// value (in WCHARs for text), its type (1 text, 0 binary), its key, then the
// value and the children, each starting on a 4-byte boundary.
func verBlock(key string, value []byte, typ, valueLen int, children ...[]byte) []byte {
	var b bytes.Buffer
	binary.Write(&b, binary.LittleEndian, [3]uint16{0, uint16(valueLen), uint16(typ)})
	b.Write(utf16z(key))
	pad4(&b)
	b.Write(value)
	for _, c := range children {
		pad4(&b)
		b.Write(c)
	}
	out := b.Bytes()
	binary.LittleEndian.PutUint16(out, uint16(len(out)))
	return out
}

func pad4(b *bytes.Buffer) {
	for b.Len()%4 != 0 {
		b.WriteByte(0)
	}
}

func utf16z(s string) []byte {
	var b bytes.Buffer
	for _, r := range utf16.Encode([]rune(s)) {
		binary.Write(&b, binary.LittleEndian, r)
	}
	b.Write([]byte{0, 0})
	return b.Bytes()
}

// writeSyso writes leaves as a COFF object holding one .rsrc section.
func writeSyso(path string, leaves []leaf) {
	// Layout of .rsrc: root dir(2 types), per-type dir, per-name dir, data entries, data.
	le := binary.LittleEndian
	var types []uint32
	byType := map[uint32][]leaf{}
	for _, l := range leaves {
		if _, ok := byType[l.typ]; !ok {
			types = append(types, l.typ)
		}
		byType[l.typ] = append(byType[l.typ], l)
	}
	dir := func(w *bytes.Buffer, n int) {
		binary.Write(w, le, [3]uint32{0, 0, 0}) // characteristics, timestamp, versions
		binary.Write(w, le, [2]uint16{0, uint16(n)})
	}
	// Compute offsets.
	off := 16 + 8*len(types)
	typeOff := map[uint32]int{}
	for _, t := range types {
		typeOff[t] = off
		off += 16 + 8*len(byType[t])
	}
	nameOff := map[*leaf]int{}
	for _, t := range types {
		for i := range byType[t] {
			nameOff[&byType[t][i]] = off
			off += 16 + 8
		}
	}
	entryOff := off
	off += 16 * len(leaves)
	dataOff := off
	var rs bytes.Buffer
	dir(&rs, len(types))
	for _, t := range types {
		binary.Write(&rs, le, [2]uint32{t, 0x80000000 | uint32(typeOff[t])})
	}
	for _, t := range types {
		dir(&rs, len(byType[t]))
		for i := range byType[t] {
			binary.Write(&rs, le, [2]uint32{byType[t][i].id, 0x80000000 | uint32(nameOff[&byType[t][i]])})
		}
	}
	idx := 0
	for _, t := range types {
		for range byType[t] {
			dir(&rs, 1)
			binary.Write(&rs, le, [2]uint32{0x409, uint32(entryOff + 16*idx)}) // en-US
			idx++
		}
	}
	// Data entries (RVA patched via relocations) and data.
	var relocs []uint32
	pos := dataOff
	var data bytes.Buffer
	for _, t := range types {
		for _, l := range byType[t] {
			relocs = append(relocs, uint32(rs.Len()))
			binary.Write(&rs, le, [4]uint32{uint32(pos), uint32(len(l.data)), 0, 0})
			data.Write(l.data)
			for data.Len()%4 != 0 {
				data.WriteByte(0)
			}
			pos = dataOff + data.Len()
		}
	}
	rs.Write(data.Bytes())

	// COFF object: header, one .rsrc section, relocations, symbol table.
	const hdr, sect = 20, 40
	rawOff := hdr + sect
	relOff := rawOff + rs.Len()
	symOff := relOff + 10*len(relocs)
	var out bytes.Buffer
	binary.Write(&out, le, [2]uint16{0x8664, 1}) // AMD64, 1 section
	binary.Write(&out, le, [3]uint32{0, uint32(symOff), 1})
	binary.Write(&out, le, [2]uint16{0, 0})
	name := [8]byte{'.', 'r', 's', 'r', 'c'}
	out.Write(name[:])
	binary.Write(&out, le, [6]uint32{0, 0, uint32(rs.Len()), uint32(rawOff), uint32(relOff), 0})
	binary.Write(&out, le, [2]uint16{uint16(len(relocs)), 0})
	binary.Write(&out, le, uint32(0x40000040)) // initialized data, read
	out.Write(rs.Bytes())
	for _, r := range relocs {
		binary.Write(&out, le, r)
		binary.Write(&out, le, uint32(0)) // symbol 0
		binary.Write(&out, le, uint16(3)) // IMAGE_REL_AMD64_ADDR32NB
	}
	sym := [8]byte{'.', 'r', 's', 'r', 'c'}
	out.Write(sym[:])
	binary.Write(&out, le, uint32(0))
	binary.Write(&out, le, [2]uint16{1, 0}) // section 1, type
	out.Write([]byte{3, 0})                 // static, no aux
	binary.Write(&out, le, uint32(4))       // empty string table

	if err := os.WriteFile(path, out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
