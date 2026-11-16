package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"github.com/xypwn/filediver/hashes"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/level"
	"github.com/xypwn/filediver/stingray/prefab"
	"github.com/xypwn/filediver/stingray/unit"
	"github.com/xypwn/filediver/stingray/unit/material"
	"sort"
	"strings"
)

func main() {
	game := flag.String("game", "C:/Program Files (x86)/Steam/steamapps/common/Helldivers 2/data", "data directory")
	flag.Parse()
	lookup := map[stingray.Hash]string{}
	thin := map[stingray.ThinHash]string{}
	for _, s := range hashes.ParseHashes(hashes.Hashes) {
		h := stingray.Sum(s)
		lookup[h] = s
		thin[h.Thin()] = s
	}
	for _, s := range hashes.ParseHashes(hashes.ThinHashes) {
		thin[stingray.Sum(s).Thin()] = s
	}
	name := func(h stingray.Hash) string {
		if s := lookup[h]; s != "" {
			return s
		}
		return h.String()
	}
	tn := func(h stingray.ThinHash) string {
		if s := thin[h]; s != "" {
			return s
		}
		return h.String()
	}
	d, e := stingray.OpenDataDir(context.Background(), *game, nil)
	if e != nil {
		panic(e)
	}
	targets := map[stingray.Hash]bool{}
	for _, a := range flag.Args() {
		h, e := stingray.ParseHash(a)
		if e != nil {
			panic(e)
		}
		targets[h] = true
		fmt.Println("UNIT", name(h))
		raw, e := d.Read(stingray.NewFileID(h, stingray.Sum("unit")), stingray.DataMain)
		if e != nil {
			fmt.Println(e)
			continue
		}
		u, e := unit.LoadInfo(bytes.NewReader(raw))
		if e != nil {
			fmt.Println(e)
			continue
		}
		fmt.Printf("terrain patches: %d; mesh count: %d\n", len(u.TerrainInfos), len(u.MeshInfos))
		fmt.Println("state machine", name(u.StateMachine), "geometry", name(u.GeometryGroup))
		for _, m := range u.MeshInfos {
			fmt.Println("mesh", tn(m.Header.MeshName))
		}
		for slot, h := range u.Materials {
			fmt.Println("material", tn(slot), name(h))
			b, e := d.Read(stingray.NewFileID(h, stingray.Sum("material")), stingray.DataMain)
			if e != nil {
				continue
			}
			m, e := material.LoadMain(bytes.NewReader(b))
			if e != nil {
				continue
			}
			fmt.Println(" shader", name(m.BaseMaterial))
			for slot, h := range m.Textures {
				fmt.Println(" texture", tn(slot), name(h))
			}
		}
	}
	matches := []string{}
	memo := map[stingray.Hash]map[stingray.Hash]bool{}
	var pf func(stingray.Hash, map[stingray.Hash]bool, int) map[stingray.Hash]bool
	pf = func(h stingray.Hash, active map[stingray.Hash]bool, depth int) map[stingray.Hash]bool {
		if m, ok := memo[h]; ok {
			return m
		}
		r := map[stingray.Hash]bool{}
		if active[h] || depth > 48 {
			return r
		}
		active[h] = true
		defer delete(active, h)
		b, e := d.Read(stingray.NewFileID(h, stingray.Sum("prefab")), stingray.DataMain)
		if e != nil {
			return r
		}
		p, e := prefab.Load(bytes.NewReader(b))
		if e != nil {
			return r
		}
		for _, u := range p.Units {
			if targets[u.Hash] {
				r[u.Hash] = true
			}
		}
		for _, n := range p.NestedPrefabs {
			for h := range pf(n.Path, active, depth+1) {
				r[h] = true
			}
		}
		memo[h] = r
		return r
	}
	for id := range d.Files {
		if id.Type != stingray.Sum("level") {
			continue
		}
		b, e := d.Read(id, stingray.DataMain)
		if e != nil {
			continue
		}
		l, e := level.LoadLevel(bytes.NewReader(b), nil)
		if e != nil {
			continue
		}
		found := map[stingray.Hash]bool{}
		for _, u := range l.Units {
			if targets[u.Hash] {
				found[u.Hash] = true
			}
		}
		for _, p := range l.EmbeddedPrefabs {
			for _, u := range p.Units {
				if targets[u.Hash] {
					found[u.Hash] = true
				}
			}
			for _, n := range p.NestedPrefabs {
				for h := range pf(n.Path, map[stingray.Hash]bool{}, 0) {
					found[h] = true
				}
			}
		}
		for _, p := range l.Prefabs {
			for h := range pf(p.Path, map[stingray.Hash]bool{}, 0) {
				found[h] = true
			}
		}
		for h := range found {
			matches = append(matches, fmt.Sprintf("%s used in %s", h, name(id.Name)))
		}
	}
	sort.Strings(matches)
	fmt.Println(strings.Join(matches, "\n"))
}
