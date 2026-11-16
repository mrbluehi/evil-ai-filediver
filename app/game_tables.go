package app

// Reads settings tables (data/game/generated_*.dl_bin) of the opened game build instead of
// the live copies embedded in filediver, so an old release build shows its own armor sets.
//
// Release builds (2024-02) ship the tables as plain DataLibrary files. Later builds seal
// them in a NaCl sealed box (X25519 + XSalsa20-Poly1305). Opening those needs the game's
// key pair, which filediver does not ship: set the environment variable
// FILEDIVER_HD2_KEYFILE to a file that holds the keys as hex (SHODAN's gamedata.lua
// works as is). The key is read at run time only and never written anywhere.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
	"golang.org/x/crypto/nacl/box"
)

// typeSizeFromTypelib returns the 64-bit size of a type in a raw dl_typelib (no caching, no
// strings needed). Layout: header 36 B, type hashes, enum hashes, type descs of 36 B
// (name offset, flags, size32, size64, ...).
func typeSizeFromTypelib(tl []byte, typeHash uint32) (int, bool) {
	if len(tl) < 36 || string(tl[:4]) != "LTLD" {
		return 0, false
	}
	nTypes := int(binary.LittleEndian.Uint32(tl[8:]))
	nEnums := int(binary.LittleEndian.Uint32(tl[12:]))
	hashes := 36
	descs := hashes + 4*nTypes + 4*nEnums
	if descs+36*nTypes > len(tl) {
		return 0, false
	}
	for i := 0; i < nTypes; i++ {
		if binary.LittleEndian.Uint32(tl[hashes+4*i:]) == typeHash {
			return int(binary.LittleEndian.Uint32(tl[descs+36*i+12:])), true
		}
	}
	return 0, false
}

var errSealedNoKey = errors.New("table is sealed and no key file is set (FILEDIVER_HD2_KEYFILE)")

type gameKeys struct {
	pk, sk [32]byte
}

func loadGameKeys() (*gameKeys, error) {
	path := os.Getenv("FILEDIVER_HD2_KEYFILE")
	if path == "" {
		return nil, nil
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	find := func(name string) ([]byte, error) {
		// e.g.  local PK = unhex('<64 hex>')  - same line, first 64-hex run after the name
		m := regexp.MustCompile(`(?m)\b` + name + `\b[^\n]*?\b([0-9a-fA-F]{64})\b`).FindSubmatch(src)
		if m == nil {
			return nil, fmt.Errorf("no %v in key file", name)
		}
		return hex.DecodeString(string(m[1]))
	}
	pk, err := find("PK")
	if err != nil {
		return nil, err
	}
	sk, err := find("SK")
	if err != nil {
		return nil, err
	}
	var k gameKeys
	copy(k.pk[:], pk)
	copy(k.sk[:], sk)
	return &k, nil
}

func looksPlainTable(b []byte) bool {
	if bytes.HasPrefix(b, []byte("LTLD")) { // typelib
		return true
	}
	i := bytes.Index(b, []byte("LDLD"))
	return i >= 0 && i < 64
}

// readGameTable returns the plaintext of data/game/<name> of the opened build.
func readGameTable(gameDir string, name string, keys *gameKeys) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(gameDir, "data", "game", name))
	if err != nil {
		return nil, err
	}
	if looksPlainTable(b) {
		return b, nil
	}
	if keys == nil {
		return nil, errSealedNoKey
	}
	plain, ok := box.OpenAnonymous(nil, b, &keys.pk, &keys.sk)
	if !ok {
		return nil, fmt.Errorf("%v: sealed box did not open with the given key", name)
	}
	return plain, nil
}

// LoadArmorSetsForGameDir reads and parses the armor kit table from an arbitrary
// Helldivers 2 install. It is used by build comparison so old-only kits are
// discoverable even when their package assets are no longer in the selected build.
// Sealed tables require FILEDIVER_HD2_KEYFILE, the same as the active-build loader.
func LoadArmorSetsForGameDir(gameDir string, strings map[uint32]string) (map[stingray.Hash]datalib.ArmorSet, error) {
	keys, err := loadGameKeys()
	if err != nil {
		return nil, err
	}
	data, err := readGameTable(gameDir, "generated_customization_armor_sets.dl_bin", keys)
	if err != nil {
		return nil, err
	}
	pieceSize := datalib.PieceInfoSize
	if typelib, err := readGameTable(gameDir, "dl_library.dl_typelib", keys); err == nil {
		if size, ok := typeSizeFromTypelib(typelib, uint32(datalib.Sum("HelldiverCustomizationPieceInfo"))); ok {
			pieceSize = size
		}
	}
	return datalib.LoadArmorSetDefinitionsFromData(data, strings, nil, pieceSize)
}
