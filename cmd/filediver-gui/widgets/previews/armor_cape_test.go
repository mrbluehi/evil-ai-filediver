package previews

import (
	datalib "github.com/xypwn/filediver/datalibrary"
	"github.com/xypwn/filediver/stingray"
	"testing"
)

func TestSharedCapeUsesSelectedKitColors(t *testing.T) {
	archive := stingray.Sum("cape-test")
	lut := stingray.Sum("cape-color-test")
	pv := UnitPreviewState{armorSets: map[stingray.Hash]datalib.ArmorSet{archive: {Archive: archive, Type: datalib.KitCape, UnitMetadata: map[stingray.Hash]datalib.UnitData{{}: {CapeLut: lut}}}}, armorContextArchives: []stingray.Hash{archive}, getSelectedArchives: func() []stingray.Hash { return nil }}
	got := pv.getArmorInfo(stingray.Sum("content/fac_helldivers/capes/medium_cape"))
	if got == nil || got.CapeLut != lut {
		t.Fatalf("shared cape should use selected kit LUT: %+v", got)
	}
}

func TestRelatedArmorKitsUseOnlyMatchingCape(t *testing.T) {
	armor := datalib.ArmorSet{Id: stingray.ThinHash{Value: 1}, SetId: 10, Archive: stingray.Hash{Value: 1}, Type: datalib.KitArmor}
	cape1 := datalib.ArmorSet{Id: stingray.ThinHash{Value: 2}, SetId: 10, Archive: stingray.Hash{Value: 2}, Type: datalib.KitCape}
	cape2 := datalib.ArmorSet{Id: stingray.ThinHash{Value: 3}, SetId: 10, Archive: stingray.Hash{Value: 3}, Type: datalib.KitCape}
	unrelated := datalib.ArmorSet{Id: stingray.ThinHash{Value: 4}, SetId: 0, Archive: stingray.Hash{Value: 4}, Type: datalib.KitCape}
	all := map[stingray.Hash]datalib.ArmorSet{armor.Archive: armor, cape1.Archive: cape1, cape2.Archive: cape2, unrelated.Archive: unrelated}
	got := relatedArmorKits(all, armor)
	if len(got) != 2 || got[1].Archive != cape1.Archive {
		t.Fatalf("automatic cape: %+v", got)
	}
	got = relatedArmorKits(all, cape2)
	if len(got) != 2 || got[1].Archive != cape2.Archive {
		t.Fatalf("selected cape: %+v", got)
	}
	got = relatedArmorKits(all, unrelated)
	if len(got) != 1 || got[0].Archive != unrelated.Archive {
		t.Fatalf("unrelated cape: %+v", got)
	}
}
