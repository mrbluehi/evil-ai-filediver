package physics

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"github.com/xypwn/filediver/stingray"
)

type Header struct {
	Type                stingray.Hash
	Filehash            stingray.Hash
	Size                uint32
	_                   [12]byte
	NameEnd             [24]byte
	Unk00               [5]uint32
	PointerListOffset   uint32
	FirstSectionOffset  uint32
	SecondSectionOffset uint32
}

type Physics struct {
	Header
	// TODO: Add data chunks
}

// ActorBounds is the local-space axis-aligned bound stored by an actor's
// Havok shape. Actor is the actor's Stingray thin hash, which normally names
// a bone in the corresponding unit skeleton.
type ActorBounds struct {
	Actor stingray.ThinHash
	Min   [3]float32
	Max   [3]float32
}

func LoadPhysics(mainR io.ReadSeeker) (*Physics, error) {
	var hdr Header
	if err := binary.Read(mainR, binary.LittleEndian, &hdr); err != nil {
		return nil, err
	}
	return &Physics{Header: hdr}, nil
}

// LoadActorBounds reads actor-local Havok shape bounds from a Stingray
// physics resource. Current Helldivers 2 resources wrap each actor's Havok
// Tagfile in the physics file. Its schema identifies the root shape's hkAabb
// field, so the field offset is not hard-coded.
// Actors without a supported AABB field are skipped; malformed offsets fail
// closed rather than producing arbitrary boxes.
func LoadActorBounds(data []byte) ([]ActorBounds, error) {
	if len(data) < 0x50 {
		return nil, errors.New("physics file is shorter than its header")
	}
	ptrList := binary.LittleEndian.Uint32(data[0x4c:0x50])
	if !rangeOK(len(data), uint64(ptrList), 8) {
		return nil, errors.New("physics actor pointer list is out of range")
	}
	count := binary.LittleEndian.Uint32(data[ptrList+4 : ptrList+8])
	if count > 1<<16 || !rangeOK(len(data), uint64(ptrList)+8, uint64(count)*4) {
		return nil, errors.New("physics actor pointer list has an invalid count")
	}

	result := make([]ActorBounds, 0, count)
	for i := uint32(0); i < count; i++ {
		rel := binary.LittleEndian.Uint32(data[ptrList+8+i*4 : ptrList+12+i*4])
		actorAt := uint64(ptrList) + uint64(rel)
		if !rangeOK(len(data), actorAt, 12) {
			continue
		}
		actorHash := stingray.ThinHash{Value: binary.LittleEndian.Uint32(data[actorAt+8 : actorAt+12])}
		if actorHash.Value == 0 {
			continue
		}

		tagAt, ok := findActorTagfile(data, actorAt)
		if !ok {
			continue
		}
		bound, ok := boundsFromTagfile(data, tagAt)
		if !ok || !validBounds(bound) {
			continue
		}
		bound.Actor = actorHash
		result = append(result, bound)
	}
	if len(result) == 0 {
		return nil, errors.New("no supported Havok actor bounds were found")
	}
	return result, nil
}

type tagChunk struct {
	name      string
	start     int
	payload   int
	end       int
	decorated bool
}

func findActorTagfile(data []byte, actorAt uint64) (int, bool) {
	start := int(actorAt)
	end := min(len(data), start+0x1000)
	if start >= end {
		return 0, false
	}
	for search := start; search+8 <= end; {
		rel := bytes.Index(data[search:end], []byte("TAG0"))
		if rel < 0 {
			return 0, false
		}
		nameAt := search + rel
		chunkAt := nameAt - 4
		if chunkAt >= start && rangeOK(len(data), uint64(chunkAt), 8) {
			chunk, ok := readChunk(data, chunkAt, len(data))
			if ok && chunk.name == "TAG0" && directChunk(data, chunk, "SDKV") != nil {
				return chunkAt, true
			}
		}
		search = nameAt + 4
	}
	return 0, false
}

func boundsFromTagfile(data []byte, tagAt int) (ActorBounds, bool) {
	root, ok := readChunk(data, tagAt, len(data))
	if !ok || root.name != "TAG0" {
		return ActorBounds{}, false
	}
	typeChunk := directChunk(data, root, "TYPE")
	dataChunk := directChunk(data, root, "DATA")
	indexChunk := directChunk(data, root, "INDX")
	if typeChunk == nil || dataChunk == nil || indexChunk == nil {
		return ActorBounds{}, false
	}
	itemChunk := directChunk(data, *indexChunk, "ITEM")
	if itemChunk == nil {
		return ActorBounds{}, false
	}
	rootType, rootDataOffset, ok := rootItem(data, *itemChunk)
	if !ok {
		return ActorBounds{}, false
	}
	fieldOffset, ok := shapeAABBOffset(data, *typeChunk, rootType)
	if !ok {
		return ActorBounds{}, false
	}
	aabbAt := uint64(dataChunk.payload) + uint64(rootDataOffset) + uint64(fieldOffset)
	if !rangeOK(dataChunk.end, aabbAt, 32) {
		return ActorBounds{}, false
	}
	var bounds ActorBounds
	for axis := 0; axis < 3; axis++ {
		bounds.Min[axis] = math.Float32frombits(binary.LittleEndian.Uint32(data[aabbAt+uint64(axis*4) : aabbAt+uint64(axis*4+4)]))
		bounds.Max[axis] = math.Float32frombits(binary.LittleEndian.Uint32(data[aabbAt+uint64(16+axis*4) : aabbAt+uint64(20+axis*4)]))
	}
	return bounds, true
}

func readChunk(data []byte, start, parentEnd int) (tagChunk, bool) {
	if start < 0 || start+8 > parentEnd || parentEnd > len(data) {
		return tagChunk{}, false
	}
	word := binary.BigEndian.Uint32(data[start : start+4])
	size := int(word & 0x00ffffff)
	if size < 8 || size > parentEnd-start {
		return tagChunk{}, false
	}
	return tagChunk{
		name:      string(data[start+4 : start+8]),
		start:     start,
		payload:   start + 8,
		end:       start + size,
		decorated: byte(word>>24) == 0x40,
	}, true
}

func directChunk(data []byte, parent tagChunk, name string) *tagChunk {
	if parent.decorated {
		return nil
	}
	for at := parent.payload; at+8 <= parent.end; {
		child, ok := readChunk(data, at, parent.end)
		if !ok {
			return nil
		}
		if child.name == name {
			return &child
		}
		at = child.end
	}
	return nil
}

func shapeAABBOffset(data []byte, typeChunk tagChunk, rootType uint32) (uint32, bool) {
	typeNamesChunk := directChunk(data, typeChunk, "TST1")
	nameTableChunk := directChunk(data, typeChunk, "TNA1")
	fieldNamesChunk := directChunk(data, typeChunk, "FST1")
	typeBodyChunk := directChunk(data, typeChunk, "TBDY")
	if typeNamesChunk == nil || nameTableChunk == nil || fieldNamesChunk == nil || typeBodyChunk == nil {
		return 0, false
	}
	typeNames := parseStringTable(data[typeNamesChunk.payload:typeNamesChunk.end])
	fieldNames := parseStringTable(data[fieldNamesChunk.payload:fieldNamesChunk.end])
	typeReader := varReader{data: data[nameTableChunk.payload:nameTableChunk.end]}
	typeCount, ok := typeReader.read()
	if !ok || typeCount > 1<<16 {
		return 0, false
	}
	classNames := make([]string, int(typeCount))
	for i := 1; i < int(typeCount); i++ {
		nameIndex, ok := typeReader.read()
		if !ok || nameIndex >= uint64(len(typeNames)) {
			return 0, false
		}
		classNames[i] = typeNames[nameIndex]
		argCount, ok := typeReader.read()
		if !ok || argCount > 1<<16 {
			return 0, false
		}
		for j := uint64(0); j < argCount; j++ {
			if _, ok = typeReader.read(); !ok {
				return 0, false
			}
			if _, ok = typeReader.read(); !ok {
				return 0, false
			}
		}
	}

	body := varReader{data: data[typeBodyChunk.payload:typeBodyChunk.end]}
	layouts := make(map[uint64]typeLayout)
	for body.pos < len(body.data) {
		typeID, ok := body.read()
		if !ok || typeID == 0 {
			break
		}
		parentID, ok := body.read()
		if !ok {
			return 0, false
		}
		options, ok := body.read()
		if !ok {
			return 0, false
		}
		layout := typeLayout{parent: parentID}
		for _, flag := range []uint64{1, 2, 4} {
			if options&flag != 0 {
				if _, ok := body.read(); !ok {
					return 0, false
				}
			}
		}
		if options&8 != 0 {
			if _, ok := body.read(); !ok { // size
				return 0, false
			}
			if _, ok := body.read(); !ok { // alignment
				return 0, false
			}
		}
		if options&16 != 0 {
			if _, ok := body.read(); !ok {
				return 0, false
			}
		}
		if options&32 != 0 {
			fieldCount, ok := body.read()
			if !ok || fieldCount > 1<<16 {
				return 0, false
			}
			for i := uint64(0); i < fieldCount; i++ {
				fieldID, ok := body.read()
				if !ok || fieldID >= uint64(len(fieldNames)) {
					return 0, false
				}
				fieldFlags, ok := body.read()
				if !ok {
					return 0, false
				}
				if fieldFlags&128 != 0 {
					if _, ok := body.read(); !ok {
						return 0, false
					}
				}
				offset, ok := body.read()
				if !ok {
					return 0, false
				}
				fieldType, ok := body.read()
				if !ok {
					return 0, false
				}
				layout.fields = append(layout.fields, typeField{
					name: fieldNames[fieldID], typ: className(classNames, fieldType), offset: offset,
				})
			}
		}
		if options&64 != 0 {
			interfaceCount, ok := body.read()
			if !ok || interfaceCount > 1<<16 {
				return 0, false
			}
			for i := uint64(0); i < interfaceCount*2; i++ {
				if _, ok := body.read(); !ok {
					return 0, false
				}
			}
		}
		layouts[typeID] = layout
	}

	return aabbFieldForClass(classNames, layouts, uint64(rootType))
}

func aabbFieldForClass(classNames []string, layouts map[uint64]typeLayout, typeID uint64) (uint32, bool) {
	if typeID >= uint64(len(classNames)) || classNames[typeID] == "" {
		return 0, false
	}
	for current, depth := typeID, 0; current != 0 && depth < 16; depth++ {
		layout, ok := layouts[current]
		if !ok {
			break
		}
		for _, field := range layout.fields {
			if field.name == "aabb" && field.typ == "hkAabb" && field.offset <= math.MaxUint32 {
				return uint32(field.offset), true
			}
		}
		current = layout.parent
	}
	return 0, false
}

type typeLayout struct {
	parent uint64
	fields []typeField
}

type typeField struct {
	name   string
	typ    string
	offset uint64
}

func className(names []string, index uint64) string {
	if index >= uint64(len(names)) {
		return ""
	}
	return names[index]
}

func rootItem(data []byte, itemChunk tagChunk) (uint32, uint32, bool) {
	payload := data[itemChunk.payload:itemChunk.end]
	if len(payload)%12 != 0 {
		return 0, 0, false
	}
	for at := 0; at+12 <= len(payload); at += 12 {
		word := binary.LittleEndian.Uint32(payload[at : at+4])
		typeID := word & 0x00ffffff
		kind := word >> 28
		offset := binary.LittleEndian.Uint32(payload[at+4 : at+8])
		count := binary.LittleEndian.Uint32(payload[at+8 : at+12])
		if typeID != 0 && kind == 1 && offset == 0 && count == 1 {
			return typeID, offset, true
		}
	}
	return 0, 0, false
}

func parseStringTable(data []byte) []string {
	parts := bytes.Split(data, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 || part[0] == 0xff {
			continue
		}
		result = append(result, string(part))
	}
	return result
}

type varReader struct {
	data []byte
	pos  int
}

func (r *varReader) read() (uint64, bool) {
	if r.pos >= len(r.data) {
		return 0, false
	}
	prefix := r.data[r.pos]
	maskMarkerLength := [...]struct {
		mask, marker byte
		length       int
	}{
		{0x80, 0x00, 1}, {0xc0, 0x80, 2}, {0xe0, 0xc0, 3}, {0xf8, 0xe0, 4},
		{0xf8, 0xe8, 5}, {0xff, 0xf8, 6}, {0xf8, 0xf0, 8}, {0xff, 0xf9, 9},
	}
	for _, entry := range maskMarkerLength {
		if prefix&entry.mask != entry.marker || r.pos+entry.length > len(r.data) {
			continue
		}
		value := uint64(prefix & ^entry.mask)
		if entry.length == 1 {
			r.pos++
			return uint64(prefix), true
		}
		for i := 1; i < entry.length; i++ {
			value = value<<8 | uint64(r.data[r.pos+i])
		}
		r.pos += entry.length
		return value, true
	}
	return 0, false
}

func validBounds(bounds ActorBounds) bool {
	for i := range 3 {
		minValue, maxValue := bounds.Min[i], bounds.Max[i]
		if math.IsNaN(float64(minValue)) || math.IsInf(float64(minValue), 0) ||
			math.IsNaN(float64(maxValue)) || math.IsInf(float64(maxValue), 0) ||
			minValue >= maxValue || maxValue-minValue > 10000 {
			return false
		}
	}
	return true
}

func rangeOK(length int, start, size uint64) bool {
	return start <= uint64(length) && size <= uint64(length)-start
}

func (b ActorBounds) String() string {
	return fmt.Sprintf("%v [%v,%v]", b.Actor, b.Min, b.Max)
}
