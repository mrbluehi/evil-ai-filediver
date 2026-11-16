package stingray

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sync"
)

const (
	hd1PackageMagic = uint32(0xf0000004)
	hd1BlockSize    = uint32(0x10000)
)

type hd1Block struct {
	compressedOffset int64
	compressedSize   uint32
	rawOffset        uint64
	rawSize          uint32
}

// hd1Package is a random-access view of one HD1 package. Its zlib blocks are
// independent, so normal resource reads only inflate the blocks they touch.
type hd1Package struct {
	path    string
	stream  string
	rawSize uint64
	blocks  []hd1Block

	cacheMu    sync.Mutex
	cachedIdx  int
	cachedData []byte
}

func hasHD1Package(dirPath string) bool {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != "" {
			continue
		}
		f, err := os.Open(filepath.Join(dirPath, entry.Name()))
		if err != nil {
			continue
		}
		var magic [4]byte
		_, readErr := io.ReadFull(f, magic[:])
		_ = f.Close()
		if readErr == nil && binary.LittleEndian.Uint32(magic[:]) == hd1PackageMagic {
			return true
		}
	}
	return false
}

func openHD1Package(dirPath, name string) (*hd1Package, error) {
	path := filepath.Join(dirPath, name)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var header [16]byte
	if _, err := io.ReadFull(f, header[:]); err != nil {
		return nil, fmt.Errorf("reading HD1 package header: %w", err)
	}
	if binary.LittleEndian.Uint32(header[0:4]) != hd1PackageMagic {
		return nil, errors.New("invalid HD1 package magic")
	}
	rawSize := uint64(binary.LittleEndian.Uint32(header[4:8]))
	if rawSize == 0 {
		return nil, errors.New("empty HD1 package")
	}
	if reserved := binary.LittleEndian.Uint32(header[8:12]); reserved != 0 {
		return nil, fmt.Errorf("invalid HD1 package reserved field: %#x", reserved)
	}

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	packageFile := &hd1Package{
		path:      path,
		stream:    filepath.Join(dirPath, name+".stream"),
		rawSize:   rawSize,
		cachedIdx: -1,
	}
	fileOffset := int64(16)
	rawOffset := uint64(0)
	chunkSize := binary.LittleEndian.Uint32(header[12:16])
	for rawOffset < rawSize {
		if chunkSize == 0 || uint64(chunkSize) > uint64(info.Size())-uint64(fileOffset) {
			return nil, fmt.Errorf("invalid HD1 package chunk size %#x at %#x", chunkSize, fileOffset)
		}
		rawChunkSize := min(uint64(hd1BlockSize), rawSize-rawOffset)
		packageFile.blocks = append(packageFile.blocks, hd1Block{
			compressedOffset: fileOffset,
			compressedSize:   chunkSize,
			rawOffset:        rawOffset,
			rawSize:          uint32(rawChunkSize),
		})
		fileOffset += int64(chunkSize)
		rawOffset += uint64(hd1BlockSize)
		if rawOffset < rawSize {
			var sizeBuf [4]byte
			if _, err := f.ReadAt(sizeBuf[:], fileOffset); err != nil {
				return nil, fmt.Errorf("reading HD1 chunk size at %#x: %w", fileOffset, err)
			}
			chunkSize = binary.LittleEndian.Uint32(sizeBuf[:])
			fileOffset += 4
		}
	}
	if fileOffset != info.Size() {
		return nil, fmt.Errorf("HD1 package has trailing or missing bytes (parsed %#x of %#x)", fileOffset, info.Size())
	}
	return packageFile, nil
}

func (p *hd1Package) decompressBlock(idx int) ([]byte, error) {
	if idx < 0 || idx >= len(p.blocks) {
		return nil, io.EOF
	}
	p.cacheMu.Lock()
	defer p.cacheMu.Unlock()
	if p.cachedIdx == idx {
		return p.cachedData, nil
	}

	block := p.blocks[idx]
	compressed := make([]byte, block.compressedSize)
	f, err := os.Open(p.path)
	if err != nil {
		return nil, err
	}
	_, err = f.ReadAt(compressed, block.compressedOffset)
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	var raw []byte
	if block.compressedSize == hd1BlockSize {
		raw = compressed
	} else {
		zr, err := zlib.NewReader(bytes.NewReader(compressed))
		if err != nil {
			return nil, fmt.Errorf("opening HD1 zlib block %d: %w", idx, err)
		}
		raw, err = io.ReadAll(io.LimitReader(zr, int64(hd1BlockSize)+1))
		closeErr := zr.Close()
		if err != nil {
			return nil, fmt.Errorf("decompressing HD1 zlib block %d: %w", idx, err)
		}
		if closeErr != nil {
			return nil, fmt.Errorf("closing HD1 zlib block %d: %w", idx, closeErr)
		}
	}
	if len(raw) < int(block.rawSize) || len(raw) > int(hd1BlockSize) {
		return nil, fmt.Errorf("invalid HD1 decompressed block size: got %d, need %d", len(raw), block.rawSize)
	}
	raw = raw[:block.rawSize]
	p.cachedIdx = idx
	p.cachedData = raw
	return raw, nil
}

func (p *hd1Package) ReadAt(dst []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("negative HD1 package offset")
	}
	if len(dst) == 0 {
		return 0, nil
	}
	if uint64(offset) >= p.rawSize {
		return 0, io.EOF
	}
	remaining := min(uint64(len(dst)), p.rawSize-uint64(offset))
	written := 0
	for remaining > 0 {
		blockIdx := int(uint64(offset) / uint64(hd1BlockSize))
		block, err := p.decompressBlock(blockIdx)
		if err != nil {
			return written, err
		}
		within := int(uint64(offset) - p.blocks[blockIdx].rawOffset)
		count := min(len(block)-within, int(remaining))
		copy(dst[written:written+count], block[within:within+count])
		written += count
		remaining -= uint64(count)
		offset += int64(count)
	}
	if written != len(dst) {
		return written, io.EOF
	}
	return written, nil
}

func openDataDirHD1(ctx context.Context, dirPath string, onProgress func(curr, total int)) (*DataDir, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, err
	}
	dataDir := &DataDir{
		Path:          dirPath,
		IsHelldivers1: true,
		hd1Packages:   make(map[Hash]*hd1Package),
		Archives:      make(map[Hash][]FileID),
		Files:         make(map[FileID][]FileInfo),
	}
	for i, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if onProgress != nil {
			onProgress(i, len(entries))
		}
		if !entry.Type().IsRegular() || filepath.Ext(entry.Name()) != "" {
			continue
		}
		archiveID, err := ParseHash(entry.Name())
		if err != nil {
			continue
		}
		packageFile, err := openHD1Package(dirPath, entry.Name())
		if err != nil {
			// The directory can contain extensionless non-package files. Ignore
			// those, but surface failures once a file claims the HD1 package magic.
			f, openErr := os.Open(filepath.Join(dirPath, entry.Name()))
			if openErr != nil {
				return nil, openErr
			}
			var magic [4]byte
			_, magicErr := io.ReadFull(f, magic[:])
			_ = f.Close()
			if magicErr == nil && binary.LittleEndian.Uint32(magic[:]) == hd1PackageMagic {
				return nil, fmt.Errorf("opening HD1 package %s: %w", entry.Name(), err)
			}
			continue
		}
		if err := indexHD1Package(dataDir, archiveID, packageFile); err != nil {
			return nil, fmt.Errorf("indexing HD1 package %s: %w", entry.Name(), err)
		}
		dataDir.hd1Packages[archiveID] = packageFile
	}
	return dataDir, nil
}

func indexHD1Package(dataDir *DataDir, archiveID Hash, packageFile *hd1Package) error {
	r := io.NewSectionReader(packageFile, 0, int64(packageFile.rawSize))
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return err
	}
	if count > 1_000_000 {
		return fmt.Errorf("unreasonable package entry count %d", count)
	}
	minimumIndexBytes := uint64(4+256) + uint64(count)*16
	if minimumIndexBytes > packageFile.rawSize {
		return fmt.Errorf("file table exceeds unpacked package size")
	}
	if _, err := r.Seek(256, io.SeekCurrent); err != nil {
		return err
	}
	fileIDs := make([]FileID, count)
	for i := range fileIDs {
		var ids [2]uint64
		if err := binary.Read(r, binary.LittleEndian, &ids); err != nil {
			return fmt.Errorf("reading file ID table item %d: %w", i, err)
		}
		fileIDs[i] = NewFileID(Hash{Value: ids[1]}, Hash{Value: ids[0]})
	}

	for i := range fileIDs {
		var ids [2]uint64
		var variantsCount uint32
		var streamOffset uint32
		if err := binary.Read(r, binary.LittleEndian, &ids); err != nil {
			return fmt.Errorf("reading entry %d ID: %w", i, err)
		}
		if err := binary.Read(r, binary.LittleEndian, &variantsCount); err != nil {
			return fmt.Errorf("reading entry %d variant count: %w", i, err)
		}
		if err := binary.Read(r, binary.LittleEndian, &streamOffset); err != nil {
			return fmt.Errorf("reading entry %d stream offset: %w", i, err)
		}
		if variantsCount > 1024 {
			return fmt.Errorf("unreasonable variant count %d in entry %d", variantsCount, i)
		}
		fileID := NewFileID(Hash{Value: ids[1]}, Hash{Value: ids[0]})
		if fileID != fileIDs[i] {
			return fmt.Errorf("entry %d does not match its file table ID", i)
		}
		type variantHeader struct {
			Unknown    uint32
			InlineSize uint32
			StreamSize uint32
		}
		variants := make([]variantHeader, variantsCount)
		for j := range variants {
			if err := binary.Read(r, binary.LittleEndian, &variants[j]); err != nil {
				return fmt.Errorf("reading entry %d variant header %d: %w", i, j, err)
			}
		}
		payloadOffset, err := r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		var mainLocus, streamLocus Locus
		var cumulativeInline uint64
		bestVariantSize := uint64(0)
		for _, variant := range variants {
			candidateSize := uint64(variant.InlineSize) + uint64(variant.StreamSize)
			if candidateSize > bestVariantSize {
				bestVariantSize = candidateSize
				mainLocus = Locus{Offset: uint64(payloadOffset) + cumulativeInline, Size: variant.InlineSize}
				streamLocus = Locus{Offset: uint64(streamOffset), Size: variant.StreamSize}
			}
			cumulativeInline += uint64(variant.InlineSize)
			if cumulativeInline > packageFile.rawSize-uint64(payloadOffset) {
				return fmt.Errorf("entry %d inline payload exceeds package", i)
			}
		}
		if _, err := r.Seek(int64(cumulativeInline), io.SeekCurrent); err != nil {
			return fmt.Errorf("skipping entry %d inline payload: %w", i, err)
		}
		if mainLocus.Size == 0 && streamLocus.Size == 0 {
			continue
		}
		file := FileInfo{ArchiveID: archiveID}
		file.Files[DataMain] = mainLocus
		file.Files[DataStream] = streamLocus
		file.Files[DataGPU] = streamLocus
		dataDir.Files[fileID] = append(dataDir.Files[fileID], file)
		dataDir.Archives[archiveID] = append(dataDir.Archives[archiveID], fileID)
	}
	return nil
}

func readNBytesHD1(d *DataDir, file FileInfo, typ DataType, nBytes int) ([]byte, error) {
	locus := file.Files[typ]
	if nBytes < 0 || nBytes > int(locus.Size) {
		return nil, fmt.Errorf("invalid HD1 read size %d for resource size %d", nBytes, locus.Size)
	}
	if typ == DataMain {
		packageFile := d.hd1Packages[file.ArchiveID]
		if packageFile == nil {
			return nil, ErrFileNotExist
		}
		data := make([]byte, nBytes)
		_, err := packageFile.ReadAt(data, int64(locus.Offset))
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	if typ != DataStream && typ != DataGPU {
		return nil, ErrFileDataTypeNotExist
	}
	streamFile, err := os.Open(d.hd1Packages[file.ArchiveID].stream)
	if err != nil {
		return nil, err
	}
	defer streamFile.Close()
	data := make([]byte, nBytes)
	if _, err := streamFile.ReadAt(data, int64(locus.Offset)); err != nil {
		return nil, err
	}
	return data, nil
}

// ResolveHelldivers1Material finds the legacy material resource that contains
// the given material slot name. HD1 mesh files store slot strings, while the
// corresponding .material resources are still addressed by hashed paths.
// Co-located resources provide a strong discriminator when the slot is reused.
func (d *DataDir) ResolveHelldivers1Material(unitID FileID, materialName ThinHash) (FileID, []byte, error) {
	if !d.IsHelldivers1 || materialName.Value == 0 {
		return FileID{}, nil, ErrFileNotExist
	}
	d.hd1MaterialIndexOnce.Do(d.buildHelldivers1MaterialIndex)
	candidates := d.hd1MaterialIndex[materialName]
	if len(candidates) == 0 {
		return FileID{}, nil, ErrFileNotExist
	}
	unitArchives := make(map[Hash]struct{}, len(d.Files[unitID]))
	for _, info := range d.Files[unitID] {
		unitArchives[info.ArchiveID] = struct{}{}
	}
	best := candidates[0]
	bestShared, bestArchiveCount := -1, -1
	for _, candidate := range candidates {
		shared := 0
		for _, info := range d.Files[candidate] {
			if _, ok := unitArchives[info.ArchiveID]; ok {
				shared++
			}
		}
		archiveCount := len(d.Files[candidate])
		if shared > bestShared || (shared == bestShared && archiveCount > bestArchiveCount) ||
			(shared == bestShared && archiveCount == bestArchiveCount && candidate.Cmp(best) < 0) {
			best, bestShared, bestArchiveCount = candidate, shared, archiveCount
		}
	}
	data, err := d.Read(best, DataMain)
	if err != nil {
		return FileID{}, nil, err
	}
	return best, data, nil
}

func (d *DataDir) buildHelldivers1MaterialIndex() {
	index := make(map[ThinHash][]FileID)
	materialType := Sum("material")
	for fileID := range d.Files {
		if fileID.Type != materialType {
			continue
		}
		data, err := d.Read(fileID, DataMain)
		if err != nil {
			continue
		}
		seen := make(map[ThinHash]struct{})
		for offset := 0; offset+4 <= len(data); offset += 4 {
			name := ThinHash{Value: binary.LittleEndian.Uint32(data[offset:])}
			if name.Value == 0 {
				continue
			}
			seen[name] = struct{}{}
		}
		for name := range seen {
			index[name] = append(index[name], fileID)
		}
	}
	for name := range index {
		slices.SortFunc(index[name], FileID.Cmp)
	}
	d.hd1MaterialIndex = index
}
