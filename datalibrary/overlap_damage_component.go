package datalib

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/xypwn/filediver/stingray"
)

type DamageDealerZoneShape int32

const (
	DamageDealerZoneShapeSphere DamageDealerZoneShape = iota
	DamageDealerZoneShapeBox
	DamageDealerZoneShapeCapsule
)

type OverlapDamageMode uint32

const (
	OverlapDamageModeImpactDamage OverlapDamageMode = iota
	OverlapDamageModeUnitSize
)

// DamageDealerZoneInfo is the authored, bone-local overlap-damage volume from
// generated_entities.dl_bin. The geometry occupies the first 48 bytes; later
// gameplay values remain separate so rendering cannot mistake them for size.
type DamageDealerZoneInfo struct {
	ZoneName             stingray.ThinHash
	Shape                DamageDealerZoneShape
	Bone                 stingray.ThinHash
	Size                 [3]float32
	LocalPosition        [3]float32
	LocalRotationDegrees [3]float32
	Mode                 OverlapDamageMode
	DamageValues         [5]float32
	UnitSize             int32
	UnitSizeValues       [5]float32
	Flags                [4]uint8
	UnknownValue         int32
	Flags2               [2]uint8
	_                    [2]uint8
	FactionType          uint32
	Flags3               [3]uint8
	_                    [5]uint8
	UnknownHashes        [8]uint64
	Flags4               [5]uint8
	_                    [3]uint8
}

type OverlapDamageComponent struct {
	Zones               [8]DamageDealerZoneInfo
	HitEffectDamageType uint32
	_                   [4]byte
}

func (c OverlapDamageComponent) ToSimple(_ HashLookup, lookupThinHash ThinHashLookup, _ StringsLookup) any {
	type simpleZone struct {
		Name         string     `json:"name"`
		Shape        string     `json:"shape"`
		Bone         string     `json:"bone"`
		Size         [3]float32 `json:"size"`
		Position     [3]float32 `json:"local_position"`
		Rotation     [3]float32 `json:"local_rotation_degrees"`
		Mode         string     `json:"mode"`
		DamageValues [5]float32 `json:"damage_values"`
		UnitSize     int32      `json:"unit_size"`
		UnitValues   [5]float32 `json:"unit_size_values"`
	}
	zones := make([]simpleZone, 0, len(c.Zones))
	for _, zone := range c.Zones {
		if zone.ZoneName.Value == 0 {
			continue
		}
		shape := "unknown"
		switch zone.Shape {
		case DamageDealerZoneShapeSphere:
			shape = "sphere"
		case DamageDealerZoneShapeBox:
			shape = "box"
		case DamageDealerZoneShapeCapsule:
			shape = "capsule"
		}
		mode := "unknown"
		switch zone.Mode {
		case OverlapDamageModeImpactDamage:
			mode = "impact damage"
		case OverlapDamageModeUnitSize:
			mode = "unit size"
		}
		zones = append(zones, simpleZone{
			Name:         lookupThinHash(zone.ZoneName),
			Shape:        shape,
			Bone:         lookupThinHash(zone.Bone),
			Size:         zone.Size,
			Position:     zone.LocalPosition,
			Rotation:     zone.LocalRotationDegrees,
			Mode:         mode,
			DamageValues: zone.DamageValues,
			UnitSize:     zone.UnitSize,
			UnitValues:   zone.UnitSizeValues,
		})
	}
	return struct {
		Zones               []simpleZone `json:"zones,omitempty"`
		HitEffectDamageType uint32       `json:"hit_effect_damage_type"`
	}{Zones: zones, HitEffectDamageType: c.HitEffectDamageType}
}

func getOverlapDamageComponentData() ([]byte, error) {
	componentHash := Sum("OverlapDamageComponentData")
	search := make([]byte, 4)
	if _, err := binary.Encode(search, binary.LittleEndian, componentHash); err != nil {
		return nil, err
	}
	index := bytes.Index(entities, search)
	if index < 0 {
		return nil, fmt.Errorf("OverlapDamageComponentData is missing from generated_entities.dl_bin")
	}
	r := bytes.NewReader(entities[index:])
	var header DLInstanceHeader
	if err := binary.Read(r, binary.LittleEndian, &header); err != nil {
		return nil, err
	}
	if header.Type != componentHash {
		return nil, fmt.Errorf("unexpected OverlapDamageComponentData header type %08x", uint32(header.Type))
	}
	if uint64(header.Size) > uint64(r.Len()) {
		return nil, fmt.Errorf("OverlapDamageComponentData payload is truncated")
	}
	data := make([]byte, header.Size)
	_, err := io.ReadFull(r, data)
	return data, err
}

func getOverlapDamageComponentDataForHash(resource stingray.Hash) ([]byte, error) {
	typelib, err := ParseTypeLib(nil)
	if err != nil {
		return nil, err
	}
	container, ok := typelib.Types[Sum("OverlapDamageComponentData")]
	if !ok || len(container.Members) != 2 {
		return nil, fmt.Errorf("OverlapDamageComponentData has an unexpected typelib layout")
	}
	if container.Members[0].TypeID != Sum("ComponentIndexData") || container.Members[1].TypeID != Sum("OverlapDamageComponent") {
		return nil, fmt.Errorf("OverlapDamageComponentData member types do not match the current typelib")
	}
	entrySize := typelib.Types[Sum("ComponentIndexData")].Size
	componentSize := typelib.Types[Sum("OverlapDamageComponent")].Size
	if entrySize != 16 || componentSize != 1544 {
		return nil, fmt.Errorf("unsupported OverlapDamageComponent layout (index=%d, component=%d)", entrySize, componentSize)
	}
	data, err := getOverlapDamageComponentData()
	if err != nil {
		return nil, err
	}
	indexCount := uint32(container.Members[0].Type.BitfieldInfoOrArrayLen.GetArrayLen())
	dataCount := uint32(container.Members[1].Type.BitfieldInfoOrArrayLen.GetArrayLen())
	indexBytes := uint64(indexCount) * uint64(entrySize)
	dataBytes := uint64(dataCount) * uint64(componentSize)
	if indexBytes+dataBytes > uint64(len(data)) {
		return nil, fmt.Errorf("OverlapDamageComponentData array sizes exceed its payload")
	}
	var componentIndex uint32
	found := false
	for i := uint32(0); i < indexCount; i++ {
		start := uint64(i) * uint64(entrySize)
		var entry ComponentIndexData
		if _, err := binary.Decode(data[start:start+uint64(entrySize)], binary.LittleEndian, &entry); err != nil {
			return nil, err
		}
		if entry.Resource == resource {
			componentIndex = entry.Index
			found = true
			break
		}
	}
	if !found || componentIndex >= dataCount {
		return nil, fmt.Errorf("%s not found in overlap-damage component data", resource.String())
	}
	start := indexBytes + uint64(componentIndex)*uint64(componentSize)
	return append([]byte(nil), data[start:start+uint64(componentSize)]...), nil
}
