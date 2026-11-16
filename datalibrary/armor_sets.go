package datalib

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"

	"github.com/xypwn/filediver/datalibrary/enum"
	"github.com/xypwn/filediver/stingray"
)

type CustomizationKitSlot uint32

const (
	SlotHelmet        CustomizationKitSlot = 0
	SlotCape          CustomizationKitSlot = 1
	SlotTorso         CustomizationKitSlot = 2
	SlotHips          CustomizationKitSlot = 3
	SlotLeftLeg       CustomizationKitSlot = 4
	SlotRightLeg      CustomizationKitSlot = 5
	SlotLeftArm       CustomizationKitSlot = 6
	SlotRightArm      CustomizationKitSlot = 7
	SlotLeftShoulder  CustomizationKitSlot = 8
	SlotRightShoulder CustomizationKitSlot = 9
)

func (b CustomizationKitSlot) String() string {
	switch b {
	case SlotHelmet:
		return "helmet"
	case SlotCape:
		return "cape"
	case SlotTorso:
		return "torso"
	case SlotHips:
		return "hips"
	case SlotLeftLeg:
		return "left_leg"
	case SlotRightLeg:
		return "right_leg"
	case SlotLeftArm:
		return "left_arm"
	case SlotRightArm:
		return "right_arm"
	case SlotLeftShoulder:
		return "left_shoulder"
	case SlotRightShoulder:
		return "right_shoulder"
	}
	return "Unknown"
}

func (p CustomizationKitSlot) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

type CustomizationKitPieceType uint32

const (
	TypeArmor        CustomizationKitPieceType = 0
	TypeUndergarment CustomizationKitPieceType = 1
	TypeAccessory    CustomizationKitPieceType = 2
)

func (b CustomizationKitPieceType) String() string {
	switch b {
	case TypeArmor:
		return "armor"
	case TypeUndergarment:
		return "undergarment"
	case TypeAccessory:
		return "accessory"
	}
	return "Unknown"
}

func (p CustomizationKitPieceType) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

type CustomizationKitWeight uint32

const (
	WeightLight  CustomizationKitWeight = 0
	WeightMedium CustomizationKitWeight = 1
	WeightHeavy  CustomizationKitWeight = 2
)

func (b CustomizationKitWeight) String() string {
	switch b {
	case WeightLight:
		return "light"
	case WeightMedium:
		return "medium"
	case WeightHeavy:
		return "heavy"
	}
	return "Unknown"
}

func (p CustomizationKitWeight) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

type Piece struct {
	Path              stingray.Hash
	Slot              CustomizationKitSlot
	Type              CustomizationKitPieceType
	Weight            CustomizationKitWeight
	_                 [4]uint8
	MaterialLut       stingray.Hash
	PatternLut        stingray.Hash
	CapeLut           stingray.Hash
	CapeGradient      stingray.Hash
	CapeNac           stingray.Hash
	DecalScalarFields stingray.Hash
	BaseData          stingray.Hash
	DecalSheet        stingray.Hash
	ToneVariations    uint8
	_                 [7]uint8
	CamouflagePieces  DLArray
}

type CustomizationKitBodyType uint32

const (
	BodyTypeStocky CustomizationKitBodyType = 0
	BodyTypeSlim   CustomizationKitBodyType = 1
	BodyTypeUnk    CustomizationKitBodyType = 2
	BodyTypeAny    CustomizationKitBodyType = 3
)

func (b CustomizationKitBodyType) String() string {
	switch b {
	case BodyTypeSlim:
		return "slim"
	case BodyTypeStocky:
		return "stocky"
	case BodyTypeUnk:
		return "unknown"
	case BodyTypeAny:
		return "any"
	}
	return "Unknown"
}

func (p CustomizationKitBodyType) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

type Body struct {
	Type   CustomizationKitBodyType
	_      [4]uint8
	Pieces DLArray
}

type CustomizationKitType uint32

const (
	KitArmor  CustomizationKitType = 0
	KitHelmet CustomizationKitType = 1
	KitCape   CustomizationKitType = 2
)

func (v CustomizationKitType) String() string {
	switch v {
	case KitArmor:
		return "Armor"
	case KitHelmet:
		return "Helmet"
	case KitCape:
		return "Cape"
	default:
		return "Unknown"
	}
}

func (p CustomizationKitType) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

type CustomizationKitRarity uint32

const (
	RarityCommon CustomizationKitRarity = 0
	// Not really sure if this is the name, but w/e
	RarityUncommon CustomizationKitRarity = 1
	RarityHeroic   CustomizationKitRarity = 2
)

func (v CustomizationKitRarity) String() string {
	switch v {
	case RarityCommon:
		return "Common"
	case RarityUncommon:
		return "Uncommon"
	case RarityHeroic:
		return "Heroic"
	default:
		return "Unknown"
	}
}

func (p CustomizationKitRarity) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

type HelldiverCustomizationKit struct {
	Id          stingray.ThinHash
	DlcId       uint32
	SetId       uint32
	NameUpper   uint32
	NameCased   uint32
	Description uint32
	Rarity      CustomizationKitRarity
	Passive     uint32
	Archive     stingray.Hash
	Type        CustomizationKitType
	Unk00       uint32
	BodyArray   DLArray
}

type CamoflagePiece struct {
	CamoflageType enum.CamouflageType
	_             [4]uint8
	MaterialLut   stingray.Hash
}

type UnitData struct {
	Slot              CustomizationKitSlot
	Type              CustomizationKitPieceType
	Weight            CustomizationKitWeight
	BodyType          CustomizationKitBodyType
	MaterialLut       stingray.Hash
	PatternLut        stingray.Hash
	CapeLut           stingray.Hash
	CapeGradient      stingray.Hash
	CapeNac           stingray.Hash
	DecalScalarFields stingray.Hash
	BaseData          stingray.Hash
	DecalSheet        stingray.Hash
	ToneVariations    uint8
	CamoflagePieces   []CamoflagePiece
}

type ArmorSet struct {
	Id           stingray.ThinHash
	DlcId        uint32
	SetId        uint32
	Name         string
	Description  string
	Rarity       CustomizationKitRarity
	Passive      *HelldiverCustomizationPassiveBonusSettings
	Type         CustomizationKitType
	Archive      stingray.Hash
	UnitMetadata map[stingray.Hash]UnitData
}

// PieceInfoSize is the size of HelldiverCustomizationPieceInfo in the live typelib (the Piece struct).
const PieceInfoSize = 112

// readPieces reads a body's piece array with the given struct stride. Known layouts:
// 72 B (2024-02/03 builds: no cape_nac/base_data/tone/camo), 96 B (2024-11 .. 2026-03),
// 112 B (live: + camouflage array). Unknown strides fall back to the 72-byte prefix.
func readPieces(arr DLArray, r *bytes.Reader, base int64, stride int) ([]Piece, error) {
	if stride == PieceInfoSize {
		return ResolveDLArray[Piece](arr, r, base)
	}
	if arr.Offset < 0 || arr.Count == 0 {
		return []Piece{}, nil
	}
	buf := make([]byte, int(arr.Count)*stride)
	if _, err := r.Seek(base+arr.Offset, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	u64 := func(b []byte, o int) stingray.Hash { return stingray.Hash{Value: binary.LittleEndian.Uint64(b[o:])} }
	u32 := func(b []byte, o int) uint32 { return binary.LittleEndian.Uint32(b[o:]) }
	out := make([]Piece, arr.Count)
	for i := range out {
		b := buf[i*stride : (i+1)*stride]
		p := Piece{Path: u64(b, 0), Slot: CustomizationKitSlot(u32(b, 8)), Type: CustomizationKitPieceType(u32(b, 12)),
			Weight: CustomizationKitWeight(u32(b, 16)), MaterialLut: u64(b, 24), PatternLut: u64(b, 32), CapeLut: u64(b, 40), CapeGradient: u64(b, 48)}
		p.CamouflagePieces = DLArray{Offset: -1}
		if stride >= 96 {
			p.CapeNac, p.DecalScalarFields, p.BaseData, p.DecalSheet, p.ToneVariations = u64(b, 56), u64(b, 64), u64(b, 72), u64(b, 80), b[88]
		} else {
			p.DecalScalarFields, p.DecalSheet = u64(b, 56), u64(b, 64)
		}
		out[i] = p
	}
	return out, nil
}

// Map of archive hash to armor set
func LoadArmorSetDefinitions(strings map[uint32]string, passives map[uint32]HelldiverCustomizationPassiveBonusSettings) (map[stingray.Hash]ArmorSet, error) {
	return LoadArmorSetDefinitionsFromData(customizationArmorSets, strings, passives, PieceInfoSize)
}

// LoadArmorSetDefinitionsFromData is LoadArmorSetDefinitions on a given (plaintext) table.
// pieceSize is that build's size of HelldiverCustomizationPieceInfo (PieceInfoSize for live).
func LoadArmorSetDefinitionsFromData(data []byte, strings map[uint32]string, passives map[uint32]HelldiverCustomizationPassiveBonusSettings, pieceSize int) (map[stingray.Hash]ArmorSet, error) {
	r := bytes.NewReader(data)

	getNameIfContained := func(casedId, upperId uint32) string {
		if name, contains := strings[casedId]; contains {
			return name
		} else if name, contains := strings[upperId]; contains {
			return name
		} else {
			return fmt.Sprintf("%x", casedId)
		}
	}

	sets := make(map[stingray.Hash]ArmorSet)
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("reading count: %v", err)
	}
	for i := uint32(0); i < count; i++ {
		var header DLSubdataHeader
		if err := binary.Read(r, binary.LittleEndian, &header); err != nil {
			return nil, fmt.Errorf("reading item %v: %v", i, err)
		}

		if header.Type != Sum("HelldiverCustomizationKit") {
			return nil, fmt.Errorf("invalid armor customization file")
		}

		base, _ := r.Seek(0, io.SeekCurrent)

		var kit HelldiverCustomizationKit
		if err := binary.Read(r, binary.LittleEndian, &kit); err != nil {
			return nil, fmt.Errorf("reading item %v: %v", i, err)
		}

		// r.Seek(base+kit.BodyArrayAddress, io.SeekStart)
		// var bodies []Body = make([]Body, kit.BodyCount)
		// if err := binary.Read(r, binary.LittleEndian, bodies); err != nil {
		// 	return nil, fmt.Errorf("reading bodies at address %x: %v", base+kit.BodyArrayAddress, err)
		// }

		bodies, err := ResolveDLArray[Body](kit.BodyArray, r, base)
		if err != nil {
			return nil, fmt.Errorf("reading bodies at address %x: %v", base+kit.BodyArray.Offset, err)
		}

		var passive *HelldiverCustomizationPassiveBonusSettings = nil
		if passives != nil {
			if passiveVal, ok := passives[kit.Passive]; ok {
				passive = &passiveVal
			}
		}

		armorSet := ArmorSet{
			Id:           kit.Id,
			DlcId:        kit.DlcId,
			SetId:        kit.SetId,
			Name:         getNameIfContained(kit.NameCased, kit.NameUpper),
			Description:  getNameIfContained(kit.Description, 0),
			Rarity:       kit.Rarity,
			Passive:      passive,
			Type:         kit.Type,
			Archive:      kit.Archive,
			UnitMetadata: make(map[stingray.Hash]UnitData),
		}

		for b, body := range bodies {
			// if _, err := r.Seek(base+body.PiecesAddress, io.SeekStart); err != nil {
			// 	return nil, fmt.Errorf("seeking piece address %x for body %v in item %v: %v", base+body.PiecesAddress, b, i, err)
			// }
			// pieces := make([]Piece, body.PiecesCount)
			// if err := binary.Read(r, binary.LittleEndian, pieces); err != nil {
			// 	return nil, fmt.Errorf("reading %v pieces for body %v in item %v (address was %x): %v", body.PiecesCount, b, i, base+body.PiecesAddress, err)
			// }
			pieces, err := readPieces(body.Pieces, r, base, pieceSize)
			if err != nil {
				return nil, fmt.Errorf("reading %v pieces for body %v in item %v (address was %x): %v", body.Pieces.Count, b, i, base+body.Pieces.Offset, err)
			}
			for p, piece := range pieces {
				camoPieces, err := ResolveDLArray[CamoflagePiece](piece.CamouflagePieces, r, base)
				if err != nil {
					return nil, fmt.Errorf("reading %v camo pieces for piece %v in body %v in item %v (address was %x): %v", piece.CamouflagePieces.Count, p, b, i, base+piece.CamouflagePieces.Offset, err)
				}
				unitData := UnitData{
					Slot:              piece.Slot,
					Type:              piece.Type,
					Weight:            piece.Weight,
					BodyType:          body.Type,
					MaterialLut:       piece.MaterialLut,
					PatternLut:        piece.PatternLut,
					CapeLut:           piece.CapeLut,
					CapeGradient:      piece.CapeGradient,
					CapeNac:           piece.CapeNac,
					DecalScalarFields: piece.DecalScalarFields,
					BaseData:          piece.BaseData,
					DecalSheet:        piece.DecalSheet,
					ToneVariations:    piece.ToneVariations,
					CamoflagePieces:   camoPieces,
				}
				armorSet.UnitMetadata[piece.Path] = unitData
			}
		}

		sets[kit.Archive] = armorSet

		r.Seek(base+int64(header.Size), io.SeekStart)
	}

	return sets, nil
}

func LoadArmorSetArray(strings map[uint32]string, passives map[uint32]HelldiverCustomizationPassiveBonusSettings) ([]ArmorSet, error) {
	r := bytes.NewReader(customizationArmorSets)

	getNameIfContained := func(casedId, upperId uint32) string {
		if name, contains := strings[casedId]; contains {
			return name
		} else if name, contains := strings[upperId]; contains {
			return name
		} else {
			return fmt.Sprintf("%x", casedId)
		}
	}

	sets := make([]ArmorSet, 0)
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("reading count: %v", err)
	}
	for i := uint32(0); i < count; i++ {
		var header DLSubdataHeader
		if err := binary.Read(r, binary.LittleEndian, &header); err != nil {
			return nil, fmt.Errorf("reading item %v: %v", i, err)
		}

		if header.Type != Sum("HelldiverCustomizationKit") {
			return nil, fmt.Errorf("invalid armor customization file")
		}

		base, _ := r.Seek(0, io.SeekCurrent)

		var kit HelldiverCustomizationKit
		if err := binary.Read(r, binary.LittleEndian, &kit); err != nil {
			return nil, fmt.Errorf("reading item %v: %v", i, err)
		}

		// r.Seek(base+kit.BodyArrayAddress, io.SeekStart)
		// var bodies []Body = make([]Body, kit.BodyCount)
		// if err := binary.Read(r, binary.LittleEndian, bodies); err != nil {
		// 	return nil, fmt.Errorf("reading bodies at address %x: %v", base+kit.BodyArrayAddress, err)
		// }

		bodies, err := ResolveDLArray[Body](kit.BodyArray, r, base)
		if err != nil {
			return nil, fmt.Errorf("reading bodies at address %x: %v", base+kit.BodyArray.Offset, err)
		}

		var passive *HelldiverCustomizationPassiveBonusSettings = nil
		if passives != nil {
			if passiveVal, ok := passives[kit.Passive]; ok {
				passive = &passiveVal
			}
		}

		armorSet := ArmorSet{
			Id:           kit.Id,
			DlcId:        kit.DlcId,
			SetId:        kit.SetId,
			Name:         getNameIfContained(kit.NameCased, kit.NameUpper),
			Description:  getNameIfContained(kit.Description, 0),
			Rarity:       kit.Rarity,
			Passive:      passive,
			Type:         kit.Type,
			Archive:      kit.Archive,
			UnitMetadata: make(map[stingray.Hash]UnitData),
		}

		for b, body := range bodies {
			// if _, err := r.Seek(base+body.PiecesAddress, io.SeekStart); err != nil {
			// 	return nil, fmt.Errorf("seeking piece address %x for body %v in item %v: %v", base+body.PiecesAddress, b, i, err)
			// }
			// pieces := make([]Piece, body.PiecesCount)
			// if err := binary.Read(r, binary.LittleEndian, pieces); err != nil {
			// 	return nil, fmt.Errorf("reading %v pieces for body %v in item %v (address was %x): %v", body.PiecesCount, b, i, base+body.PiecesAddress, err)
			// }
			pieces, err := ResolveDLArray[Piece](body.Pieces, r, base)
			if err != nil {
				return nil, fmt.Errorf("reading %v pieces for body %v in item %v (address was %x): %v", body.Pieces.Count, b, i, base+body.Pieces.Offset, err)
			}
			for p, piece := range pieces {
				camoPieces, err := ResolveDLArray[CamoflagePiece](piece.CamouflagePieces, r, base)
				if err != nil {
					return nil, fmt.Errorf("reading %v camo pieces for piece %v in body %v in item %v (address was %x): %v", piece.CamouflagePieces.Count, p, b, i, base+piece.CamouflagePieces.Offset, err)
				}
				unitData := UnitData{
					Slot:              piece.Slot,
					Type:              piece.Type,
					Weight:            piece.Weight,
					BodyType:          body.Type,
					MaterialLut:       piece.MaterialLut,
					PatternLut:        piece.PatternLut,
					CapeLut:           piece.CapeLut,
					CapeGradient:      piece.CapeGradient,
					CapeNac:           piece.CapeNac,
					DecalScalarFields: piece.DecalScalarFields,
					BaseData:          piece.BaseData,
					DecalSheet:        piece.DecalSheet,
					ToneVariations:    piece.ToneVariations,
					CamoflagePieces:   camoPieces,
				}
				armorSet.UnitMetadata[piece.Path] = unitData
			}
		}

		sets = append(sets, armorSet)

		r.Seek(base+int64(header.Size), io.SeekStart)
	}

	return sets, nil
}
