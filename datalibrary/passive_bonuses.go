package datalib

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strings"

	"github.com/xypwn/filediver/stingray"
)

type HelldiverCustomizationPassiveBonusModifierType uint32

const (
	ModifierTypeSet HelldiverCustomizationPassiveBonusModifierType = iota
	ModifierTypeAdd
	ModifierTypeMultiply
	ModifierTypeTime
)

func (p HelldiverCustomizationPassiveBonusModifierType) MarshalText() ([]byte, error) {
	return []byte(p.String()), nil
}

//go:generate go run golang.org/x/tools/cmd/stringer -type=HelldiverCustomizationPassiveBonusModifierType

type rawHelldiverCustomizationPassiveBonusModifier struct {
	ModifierId   stingray.ThinHash
	ModifierType HelldiverCustomizationPassiveBonusModifierType
	Value        float32
	Description  uint32
}

type HelldiverCustomizationPassiveBonusModifier struct {
	ModifierId   string                                         `json:"modifier_id"`
	ModifierType HelldiverCustomizationPassiveBonusModifierType `json:"modifier_type"`
	Value        float32                                        `json:"value"`
	Description  string                                         `json:"description"`
}

func (modifier HelldiverCustomizationPassiveBonusModifier) ResolveDescription() string {
	var desc string
	switch modifier.ModifierType {
	case ModifierTypeAdd:
		desc = strings.ReplaceAll(modifier.Description, "#BONUS", fmt.Sprintf("%d", int(modifier.Value)))
		desc = strings.ReplaceAll(desc, "#SIGN", "+")
	case ModifierTypeMultiply:
		percent := int(math.Round(math.Abs(float64(modifier.Value-1.0) * 100)))
		desc = strings.ReplaceAll(modifier.Description, "#BONUS", fmt.Sprintf("%d%%", percent))
		if math.Signbit(float64(modifier.Value - 1.0)) {
			desc = strings.ReplaceAll(desc, "#SIGN", "-")
		} else {
			desc = strings.ReplaceAll(desc, "#SIGN", "+")
		}
	case ModifierTypeTime:
		desc = strings.ReplaceAll(modifier.Description, "#BONUS", fmt.Sprintf("%.1f seconds", modifier.Value))
	case ModifierTypeSet:
		desc = modifier.Description
	}
	return desc
}

type HelldiverCustomizationStatModifier struct {
	Stat      uint32  `json:"stat"`
	UnkFloat1 float32 `json:"unkfloat1"`
	UnkFloat2 float32 `json:"unkfloat2"`
}

type rawHelldiverCustomizationPassiveBonusSettings struct {
	PassiveBonus          uint32
	Name                  uint32
	Icon                  stingray.Hash
	PassiveModifierOffset int64
	PassiveModifierCount  int64
	StatModifierOffset    int64
	StatModifierCount     int64
	SomeHash              stingray.ThinHash
}

type HelldiverCustomizationPassiveBonusSettings struct {
	PassiveBonus     uint32                                       `json:"id"`
	Name             string                                       `json:"name"`
	Icon             string                                       `json:"icon"`
	PassiveModifiers []HelldiverCustomizationPassiveBonusModifier `json:"passive_modifiers"`
	StatModifiers    []HelldiverCustomizationStatModifier         `json:"stat_modifiers,omitempty"`
	SomeString       string                                       `json:"some_string"`
}

func (passive *HelldiverCustomizationPassiveBonusSettings) ResolveDescription() []string {
	passiveDescription := make([]string, 0)
	for _, modifier := range passive.PassiveModifiers {
		passiveDescription = append(passiveDescription, modifier.ResolveDescription())
	}
	return passiveDescription
}

func LoadPassiveBonusDefinitions(lookupHash HashLookup, lookupThinHash ThinHashLookup, lookupStrings StringsLookup) (map[uint32]HelldiverCustomizationPassiveBonusSettings, error) {
	return LoadPassiveBonusDefinitionsFromData(customizationPassiveBonuses, lookupHash, lookupThinHash, lookupStrings, PassiveBonusSettingsSize)
}

// PassiveBonusSettingsSize is the live size of HelldiverCustomizationPassiveBonusSettings.
// Older builds: 32 B (2024-02/03: no stat_modifiers), 48 B (2024-11: no trailing u32).
const PassiveBonusSettingsSize = 56

// LoadPassiveBonusDefinitionsFromData is LoadPassiveBonusDefinitions on a given (plaintext) table;
// settingsSize is that build's size of HelldiverCustomizationPassiveBonusSettings.
func LoadPassiveBonusDefinitionsFromData(data []byte, lookupHash HashLookup, lookupThinHash ThinHashLookup, lookupStrings StringsLookup, settingsSize int) (map[uint32]HelldiverCustomizationPassiveBonusSettings, error) {
	r := bytes.NewReader(data)

	definitions := make(map[uint32]HelldiverCustomizationPassiveBonusSettings)
	var count uint32
	if err := binary.Read(r, binary.LittleEndian, &count); err != nil {
		return nil, fmt.Errorf("reading count: %v", err)
	}
	for i := uint32(0); i < count; i++ {
		var header DLSubdataHeader
		if err := binary.Read(r, binary.LittleEndian, &header); err != nil {
			return nil, fmt.Errorf("reading item %v: %v", i, err)
		}

		if header.Type != Sum("HelldiverCustomizationPassiveBonusSettings") {
			return nil, fmt.Errorf("invalid passive bonus file")
		}

		base, _ := r.Seek(0, io.SeekCurrent)

		var passive rawHelldiverCustomizationPassiveBonusSettings
		if settingsSize >= 48 {
			if err := binary.Read(r, binary.LittleEndian, &passive); err != nil {
				return nil, fmt.Errorf("reading item %v: %v", i, err)
			}
		} else { // 32 B layout: passive_bonus, name_loc, icon, modifiers[]
			var old struct {
				PassiveBonus          uint32
				Name                  uint32
				Icon                  stingray.Hash
				PassiveModifierOffset int64
				PassiveModifierCount  int64
			}
			if err := binary.Read(r, binary.LittleEndian, &old); err != nil {
				return nil, fmt.Errorf("reading item %v: %v", i, err)
			}
			passive = rawHelldiverCustomizationPassiveBonusSettings{PassiveBonus: old.PassiveBonus, Name: old.Name, Icon: old.Icon,
				PassiveModifierOffset: old.PassiveModifierOffset, PassiveModifierCount: old.PassiveModifierCount, StatModifierOffset: -1}
		}
		if passive.StatModifierCount < 0 || passive.StatModifierOffset < 0 {
			passive.StatModifierCount = 0
		}

		r.Seek(base+passive.PassiveModifierOffset, io.SeekStart)
		var rawPassiveModifiers []rawHelldiverCustomizationPassiveBonusModifier = make([]rawHelldiverCustomizationPassiveBonusModifier, passive.PassiveModifierCount)
		if err := binary.Read(r, binary.LittleEndian, rawPassiveModifiers); err != nil {
			return nil, fmt.Errorf("reading passiveModifiers at address %x: %v", base+passive.PassiveModifierOffset, err)
		}

		passiveModifiers := make([]HelldiverCustomizationPassiveBonusModifier, 0)
		for _, modifier := range rawPassiveModifiers {
			passiveModifiers = append(passiveModifiers, HelldiverCustomizationPassiveBonusModifier{
				ModifierId:   lookupThinHash(modifier.ModifierId),
				ModifierType: modifier.ModifierType,
				Value:        modifier.Value,
				Description:  lookupStrings(modifier.Description),
			})
		}

		var statModifiers []HelldiverCustomizationStatModifier = make([]HelldiverCustomizationStatModifier, passive.StatModifierCount)
		if passive.StatModifierCount > 0 {
			r.Seek(base+passive.StatModifierOffset, io.SeekStart)
			if err := binary.Read(r, binary.LittleEndian, statModifiers); err != nil {
				return nil, fmt.Errorf("reading statModifiers at address %x: %v", base+passive.StatModifierOffset, err)
			}
		}

		definitions[passive.PassiveBonus] = HelldiverCustomizationPassiveBonusSettings{
			PassiveBonus:     passive.PassiveBonus,
			Name:             lookupStrings(passive.Name),
			Icon:             lookupHash(passive.Icon),
			PassiveModifiers: passiveModifiers,
			StatModifiers:    statModifiers,
			SomeString:       lookupThinHash(passive.SomeHash),
		}

		r.Seek(base+int64(header.Size), io.SeekStart)
	}

	return definitions, nil
}
