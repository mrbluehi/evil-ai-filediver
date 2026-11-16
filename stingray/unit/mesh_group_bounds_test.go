package unit

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestLoadMeshExpandsUnderreportedGroupVertices(t *testing.T) {
	var buf bytes.Buffer
	for _, p := range [][3]float32{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}, {1, 1, 0}} {
		if err := binary.Write(&buf, binary.LittleEndian, p); err != nil {
			t.Fatal(err)
		}
	}
	for _, index := range []uint16{0, 1, 3} {
		if err := binary.Write(&buf, binary.LittleEndian, index); err != nil {
			t.Fatal(err)
		}
	}
	layout := MeshLayout{NumItems: 1, NumVertices: 4, VertexStride: 12, NumIndices: 3, IndexOffset: 48, IndicesSize: 6}
	layout.Items[0] = MeshLayoutItem{Type: ItemPosition, Format: FormatVec3F}
	info := MeshInfo{Groups: []MeshGroup{{NumVertices: 3, NumIndices: 3}}}
	mesh, err := loadMesh(bytes.NewReader(buf.Bytes()), info, layout)
	if err != nil {
		t.Fatal(err)
	}
	if len(mesh.Positions) != 4 || mesh.Indices[0][2] != 3 {
		t.Fatalf("positions=%d indices=%v", len(mesh.Positions), mesh.Indices)
	}
}
