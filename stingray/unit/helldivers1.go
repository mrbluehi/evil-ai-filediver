package unit

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/go-gl/mathgl/mgl32"
	"github.com/xypwn/filediver/stingray"
)

type helldivers1Reader struct {
	data []byte
	off  int
}

func (r *helldivers1Reader) readU32() (uint32, error) {
	if r.off < 0 || len(r.data)-r.off < 4 {
		return 0, fmt.Errorf("unexpected end of Helldivers 1 unit at %#x", r.off)
	}
	value := binary.LittleEndian.Uint32(r.data[r.off:])
	r.off += 4
	return value, nil
}

func (r *helldivers1Reader) take(size uint64) ([]byte, error) {
	if size > uint64(len(r.data)) || uint64(r.off)+size > uint64(len(r.data)) {
		return nil, fmt.Errorf("Helldivers 1 unit range %#x..%#x exceeds file size %#x", r.off, uint64(r.off)+size, len(r.data))
	}
	result := r.data[r.off : r.off+int(size)]
	r.off += int(size)
	return result, nil
}

type helldivers1Element struct {
	Type uint32
	Size uint32
}

type helldivers1Shape struct {
	MaterialIndex uint32
	IndexOffset   uint32
	TriangleCount uint32
	ShapeIndex    uint32
}

// IsHelldivers1Unit reports whether data starts with the older HD1 mesh unit
// layout. In these files the first word is the mesh header size (0x90).
func IsHelldivers1Unit(data []byte) bool {
	return len(data) >= 8 && binary.LittleEndian.Uint32(data[:4]) == 0x90
}

// LoadHelldivers1 decodes the mesh records used by Helldivers 1. Unlike the
// HD2 unit format, HD1 stores vertex and index buffers directly in each unit.
func LoadHelldivers1(data []byte) (*Info, map[string]Mesh, error) {
	r := &helldivers1Reader{data: data}
	signature, err := r.readU32()
	if err != nil {
		return nil, nil, err
	}
	if signature != 0x90 {
		return nil, nil, fmt.Errorf("unexpected Helldivers 1 unit header size %#x", signature)
	}
	meshCount, err := r.readU32()
	if err != nil {
		return nil, nil, err
	}
	if meshCount > 4096 {
		return nil, nil, fmt.Errorf("unreasonable Helldivers 1 mesh count %d", meshCount)
	}

	info := &Info{
		Materials:     make(map[stingray.ThinHash]stingray.Hash),
		MaterialNames: make(map[stingray.ThinHash]string),
		Bones: []Bone{{
			Index:       0,
			ParentIndex: 0,
			Matrix:      mgl32.Ident4(),
		}},
	}
	meshes := make(map[string]Mesh, meshCount)
	for meshIdx := uint32(0); meshIdx < meshCount; meshIdx++ {
		mesh, err := readHelldivers1Mesh(r, meshIdx, info)
		if err != nil {
			return nil, nil, fmt.Errorf("mesh %d: %w", meshIdx, err)
		}
		name := fmt.Sprintf("mesh_%03d", meshIdx)
		meshes[name] = mesh
		info.MeshInfos = append(info.MeshInfos, mesh.Info)
	}
	info.NumMeshes = uint32(len(meshes))
	return info, meshes, nil
}

func readHelldivers1Mesh(r *helldivers1Reader, meshIdx uint32, unitInfo *Info) (Mesh, error) {
	var mesh Mesh
	elementBufferSize, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	if elementBufferSize > uint32(len(r.data)) {
		return mesh, fmt.Errorf("unreasonable vertex buffer size %d", elementBufferSize)
	}
	elementBufferOffset := r.off
	if _, err := r.take(uint64(elementBufferSize)); err != nil {
		return mesh, err
	}
	if _, err := r.take(8); err != nil { // reserved words
		return mesh, err
	}
	vertexCount, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	stride, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	elementCount, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	if vertexCount == 0 || vertexCount > 10_000_000 || stride == 0 || elementCount > 32 {
		return mesh, fmt.Errorf("invalid vertex layout: vertices=%d stride=%d elements=%d", vertexCount, stride, elementCount)
	}
	if uint64(vertexCount)*uint64(stride) > uint64(elementBufferSize) {
		return mesh, fmt.Errorf("vertex buffer is too small: %d bytes for %d vertices with stride %d", elementBufferSize, vertexCount, stride)
	}
	elements := make([]helldivers1Element, elementCount)
	for i := range elements {
		elements[i].Type, err = r.readU32()
		if err != nil {
			return mesh, err
		}
		elements[i].Size, err = r.readU32()
		if err != nil {
			return mesh, err
		}
		if _, err := r.take(9); err != nil { // engine-specific flags and end marker
			return mesh, err
		}
	}
	if _, err := r.take(12); err != nil {
		return mesh, err
	}
	totalIndexCount, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	indexBufferSize, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	indexBuffer, err := r.take(uint64(indexBufferSize))
	if err != nil {
		return mesh, err
	}
	shapeCount, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	if shapeCount > 1_000_000 {
		return mesh, fmt.Errorf("unreasonable shape count %d", shapeCount)
	}
	shapes := make([]helldivers1Shape, shapeCount)
	for i := range shapes {
		if _, err := r.take(16); err != nil {
			return mesh, err
		}
		shapeData := r.data[r.off-16 : r.off]
		shapes[i] = helldivers1Shape{
			MaterialIndex: binary.LittleEndian.Uint32(shapeData[0:4]),
			IndexOffset:   binary.LittleEndian.Uint32(shapeData[4:8]),
			TriangleCount: binary.LittleEndian.Uint32(shapeData[8:12]),
			ShapeIndex:    binary.LittleEndian.Uint32(shapeData[12:16]),
		}
	}
	if _, err := r.take(28); err != nil { // unknown bounds and unit data
		return mesh, err
	}
	materialCount, err := r.readU32()
	if err != nil {
		return mesh, err
	}
	if materialCount > 65536 {
		return mesh, fmt.Errorf("unreasonable material count %d", materialCount)
	}
	materialHashes := make([]stingray.ThinHash, materialCount)
	for i := range materialHashes {
		nameLength, err := r.readU32()
		if err != nil {
			return mesh, err
		}
		if nameLength > 1<<20 {
			return mesh, fmt.Errorf("unreasonable material name length %d", nameLength)
		}
		nameBytes, err := r.take(uint64(nameLength))
		if err != nil {
			return mesh, err
		}
		nameHash := stingray.Sum(string(nameBytes))
		materialHashes[i] = nameHash.Thin()
		unitInfo.Materials[nameHash.Thin()] = nameHash
		unitInfo.MaterialNames[nameHash.Thin()] = string(nameBytes)
	}
	if len(materialHashes) == 0 {
		materialHashes = append(materialHashes, stingray.ThinHash{})
		unitInfo.Materials[stingray.ThinHash{}] = stingray.Hash{}
	}

	mesh.Positions = make([][3]float32, vertexCount)
	mesh.Normals = make([][3]float32, vertexCount)
	mesh.Tangents = make([][4]float32, vertexCount)
	mesh.Bitangents = make([][3]float32, vertexCount)
	mesh.UVCoords = [][][2]float32{make([][2]float32, vertexCount)}
	hasPosition, hasNormal, hasUV := false, false, false
	for vertex := uint32(0); vertex < vertexCount; vertex++ {
		// This is the per-vertex offset within one stride. The vertex's base
		// address is added separately below.
		attributeOffset := uint64(0)
		for _, element := range elements {
			componentCount := uint64(element.Size + 1)
			byteCount := componentCount * 4
			switch element.Type {
			case 0, 1, 2, 3:
				byteCount = 12
			case 4:
				byteCount = 8
			case 5:
				byteCount = 16
			case 6, 7:
			default:
				return mesh, fmt.Errorf("unsupported vertex element type %d", element.Type)
			}
			if attributeOffset+byteCount > uint64(stride) {
				return mesh, fmt.Errorf("vertex element %d exceeds stride %d", element.Type, stride)
			}
			start := uint64(elementBufferOffset) + uint64(vertex)*uint64(stride) + attributeOffset
			value := r.data[start : start+byteCount]
			readFloat := func(component int) float32 {
				return math.Float32frombits(binary.LittleEndian.Uint32(value[component*4:]))
			}
			switch element.Type {
			case 0:
				mesh.Positions[vertex] = [3]float32{readFloat(0), readFloat(1), readFloat(2)}
				hasPosition = true
			case 1:
				mesh.Normals[vertex] = [3]float32{readFloat(0), readFloat(1), readFloat(2)}
				hasNormal = true
			case 2:
				mesh.Tangents[vertex] = [4]float32{readFloat(0), readFloat(1), readFloat(2), 1}
			case 3:
				mesh.Bitangents[vertex] = [3]float32{readFloat(0), readFloat(1), readFloat(2)}
			case 4:
				// HD1's UV convention is vertically inverted relative to the
				// OpenGL preview textures.
				mesh.UVCoords[0][vertex] = [2]float32{readFloat(0), -readFloat(1)}
				hasUV = true
			}
			attributeOffset += byteCount
		}
	}
	if !hasPosition {
		return mesh, fmt.Errorf("mesh has no position element")
	}
	if !hasUV {
		mesh.UVCoords[0] = make([][2]float32, vertexCount)
	}
	if !hasNormal {
		mesh.Normals = computeHelldivers1Normals(mesh.Positions, indexBuffer, shapes)
	}
	for i := range mesh.Tangents {
		if mesh.Tangents[i] == ([4]float32{}) {
			mesh.Tangents[i] = [4]float32{1, 0, 0, 1}
		}
	}

	indexCountFromBuffer := uint64(indexBufferSize / 2)
	if indexBufferSize%2 != 0 || uint64(totalIndexCount) > indexCountFromBuffer {
		return mesh, fmt.Errorf("invalid index buffer: %d bytes, %d indices", indexBufferSize, totalIndexCount)
	}
	if len(shapes) == 0 && totalIndexCount > 0 {
		shapes = append(shapes, helldivers1Shape{TriangleCount: totalIndexCount / 3})
	}
	mesh.Indices = make([][]uint32, len(shapes))
	mesh.Info.Groups = make([]MeshGroup, len(shapes))
	mesh.Info.Materials = materialHashes
	for group, shape := range shapes {
		firstIndex := uint64(shape.IndexOffset) * 3
		indexCount := uint64(shape.TriangleCount) * 3
		if firstIndex+indexCount > indexCountFromBuffer || firstIndex+indexCount > uint64(totalIndexCount) {
			return mesh, fmt.Errorf("shape %d index range exceeds index buffer", group)
		}
		indices := make([]uint32, indexCount)
		for i := range indices {
			index := binary.LittleEndian.Uint16(indexBuffer[(firstIndex+uint64(i))*2:])
			if uint32(index) >= vertexCount {
				return mesh, fmt.Errorf("shape %d vertex index %d exceeds vertex count %d", group, index, vertexCount)
			}
			indices[i] = uint32(index)
		}
		mesh.Indices[group] = indices
		materialIndex := shape.MaterialIndex
		if int(materialIndex) >= len(mesh.Info.Materials) {
			materialIndex = 0
		}
		mesh.Info.Groups[group] = MeshGroup{
			MaterialIdx:  materialIndex,
			VertexOffset: 0,
			NumVertices:  vertexCount,
			IndexOffset:  uint32(firstIndex),
			NumIndices:   uint32(indexCount),
			GroupIdx:     shape.ShapeIndex,
		}
	}

	minPos := mesh.Positions[0]
	maxPos := mesh.Positions[0]
	for _, position := range mesh.Positions[1:] {
		for axis := range 3 {
			minPos[axis] = min(minPos[axis], position[axis])
			maxPos[axis] = max(maxPos[axis], position[axis])
		}
	}
	meshName := stingray.Sum(fmt.Sprintf("mesh_%03d", meshIdx)).Thin()
	mesh.Info.Header = MeshHeader{
		AABB:               AABB{Min: minPos, Max: maxPos},
		MeshName:           meshName,
		TransformIdx:       0,
		AABBTransformIndex: 0,
	}
	return mesh, nil
}

func computeHelldivers1Normals(positions [][3]float32, indexBuffer []byte, shapes []helldivers1Shape) [][3]float32 {
	normals := make([][3]float32, len(positions))
	for _, shape := range shapes {
		first := uint64(shape.IndexOffset) * 3
		count := uint64(shape.TriangleCount) * 3
		if (first+count)*2 > uint64(len(indexBuffer)) {
			continue
		}
		for i := uint64(0); i+2 < count; i += 3 {
			ia := binary.LittleEndian.Uint16(indexBuffer[(first+i)*2:])
			ib := binary.LittleEndian.Uint16(indexBuffer[(first+i+1)*2:])
			ic := binary.LittleEndian.Uint16(indexBuffer[(first+i+2)*2:])
			if int(ia) >= len(positions) || int(ib) >= len(positions) || int(ic) >= len(positions) {
				continue
			}
			a, b, c := positions[ia], positions[ib], positions[ic]
			ab := [3]float32{b[0] - a[0], b[1] - a[1], b[2] - a[2]}
			ac := [3]float32{c[0] - a[0], c[1] - a[1], c[2] - a[2]}
			normal := [3]float32{
				ab[1]*ac[2] - ab[2]*ac[1],
				ab[2]*ac[0] - ab[0]*ac[2],
				ab[0]*ac[1] - ab[1]*ac[0],
			}
			for _, idx := range []uint16{ia, ib, ic} {
				for axis := range 3 {
					normals[idx][axis] += normal[axis]
				}
			}
		}
	}
	for i := range normals {
		length := float32(math.Sqrt(float64(normals[i][0]*normals[i][0] + normals[i][1]*normals[i][1] + normals[i][2]*normals[i][2])))
		if length == 0 {
			normals[i] = [3]float32{0, 0, 1}
			continue
		}
		for axis := range 3 {
			normals[i][axis] /= length
		}
	}
	return normals
}
