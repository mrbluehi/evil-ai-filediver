// path-discovery matches literal path candidates against a game's actual resource hashes.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/xypwn/filediver/stingray"
)

func main() {
	kind := flag.String("type", "unit", "resource type to target")
	game := flag.String("game", "", "game contents directory")
	source := flag.String("source", "", "file or directory containing candidate text")
	recombine := flag.Bool("recombine", false, "test known directory prefix and path suffix combinations")
	wide := flag.Bool("wide", false, "include directories from other known resource types")
	generate := flag.Bool("generate", false, "test sibling unit paths and naming variants")
	scan := flag.Bool("scan", false, "scan decoded Lua resources for literal paths")
	knownFile := flag.String("known", "hashes/hashes.txt", "existing path dictionary")
	out := flag.String("out", "verified-paths.tsv", "output report")
	flag.Parse()
	if *game == "" || (*source == "" && !*scan && !*generate) {
		flag.Usage()
		os.Exit(2)
	}
	d, err := stingray.OpenDataDir(context.Background(), *game, nil)
	if err != nil {
		panic(err)
	}
	targets := make(map[stingray.Hash]bool)
	for id := range d.Files {
		if id.Type == stingray.Sum(*kind) {
			targets[id.Name] = true
		}
	}
	known := make(map[stingray.Hash]bool)
	unitPaths := []string{}
	allPaths := []string{}
	if data, err := os.ReadFile(*knownFile); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			s := strings.TrimSpace(line)
			if s != "" && !strings.HasPrefix(s, "//") && !strings.HasPrefix(s, "#") {
				known[stingray.Sum(s)] = true
				if strings.HasPrefix(s, "content/") {
					allPaths = append(allPaths, s)
				}
				if targets[stingray.Sum(s)] && strings.HasPrefix(s, "content/") {
					unitPaths = append(unitPaths, s)
				}
			}
		}
	}
	resolved := 0
	for h := range targets {
		if known[h] {
			resolved++
		}
	}
	fmt.Printf("%s: %d resources, %d known paths, %d unknown\n", *kind, len(targets), resolved, len(targets)-resolved)
	re := regexp.MustCompile(`(?:content|core)/[a-zA-Z0-9_./-]+`)
	found := make(map[string]string)
	inspect := func(data []byte, origin string) {
		for _, raw := range re.FindAll(data, -1) {
			p := strings.TrimRight(string(raw), ".-/")
			candidates := []string{p}
			if i := strings.LastIndexByte(p, '.'); i > strings.LastIndexByte(p, '/') {
				candidates = append(candidates, p[:i])
			}
			for _, candidate := range candidates {
				h := stingray.Sum(candidate)
				if targets[h] && !known[h] {
					if _, exists := found[candidate]; !exists {
						found[candidate] = origin
					}
				}
			}
		}
	}
	if *generate {
		dirs, leaves := map[string]bool{}, map[string]bool{}
		for _, p := range unitPaths {
			i := strings.LastIndexByte(p, '/')
			if i < 0 {
				continue
			}
			dirs[p[:i]] = true
			leaves[p[i+1:]] = true
		}

		if *wide {
			for _, p := range allPaths {
				if strings.Contains(p, "/audio/") || strings.Contains(p, "/animations/") {
					continue
				}
				dir := p[:strings.LastIndexByte(p, '/')]
				for _, folder := range []string{"/textures", "/materials", "/models"} {
					dir = strings.TrimSuffix(dir, folder)
				}
				dirs[dir] = true
				resourceLeaf := p[strings.LastIndexByte(p, '/')+1:]
				for _, suffix := range []string{"_albedo", "_normal", "_nac", "_nar", "_basecolor", "_color", "_diffuse", "_roughness", "_material"} {
					resourceLeaf = strings.TrimSuffix(resourceLeaf, suffix)
				}
				if len(resourceLeaf) > 3 {
					leaves[resourceLeaf] = true
				}
				leaf := dir[strings.LastIndexByte(dir, '/')+1:]
				if len(leaf) > 3 {
					leaves[leaf] = true
				}
			}
		}
		tested := 0
		add := func(p, origin string) {
			tested++
			h := stingray.Sum(p)
			if targets[h] && !known[h] {
				if _, ok := found[p]; !ok {
					found[p] = origin
				}
			}
		}
		for dir := range dirs {
			for leaf := range leaves {
				add(dir+"/"+leaf, "verified-unit sibling pattern")
			}
		}
		if *recombine {
			prefixes, suffixes := map[string]bool{}, map[string]bool{}
			for _, p := range allPaths {
				if strings.Contains(p, "/audio/") {
					continue
				}
				for i, c := range p {
					if c == '/' && i > len("content/") {
						prefixes[p[:i]] = true
						suffixes[p[i+1:]] = true
					}
				}
			}
			for prefix := range prefixes {
				for suffix := range suffixes {
					add(prefix+"/"+suffix, "known path prefix/suffix recombination")
				}
			}
		}
		for _, p := range unitPaths {
			for _, suffix := range []string{"_a", "_b", "_c", "_d", "_fake", "_static", "_dynamic", "_destroyed", "_damaged", "_broken", "_intact", "_debris", "_left", "_right", "_lod", "_01", "_02", "_03"} {
				add(p+suffix, "verified-unit suffix pattern")
			}
			for i := 0; i <= 30; i++ {
				add(fmt.Sprintf("%s_%02d", p, i), "verified-unit numbered pattern")
				add(fmt.Sprintf("%s_%d", p, i), "verified-unit numbered pattern")
			}
		}
		fmt.Printf("Tested %d unit path candidates\n", tested)
	}
	if *scan {
		scanned := 0
		for id := range d.Files {
			if id.Type != stingray.Sum("lua") {
				continue
			}
			data, err := d.Read(id, stingray.DataMain)
			if err != nil {
				fmt.Fprintln(os.Stderr, id, err)
				continue
			}
			inspect(data, id.Name.String()+".lua")
			scanned++
		}
		fmt.Printf("Scanned %d Lua resources\n", scanned)
	}
	if *source != "" {
		err = filepath.WalkDir(*source, func(path string, ent os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if ent.IsDir() {
				return nil
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			buf := make([]byte, 1<<20)
			tail := []byte{}
			for {
				n, err := f.Read(buf)
				if n > 0 {
					data := append(tail, buf[:n]...)
					inspect(data, path)
					keep := min(len(data), 4096)
					tail = append([]byte(nil), data[len(data)-keep:]...)
				}
				if err == io.EOF {
					break
				}
				if err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			panic(err)
		}
	}
	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	f, err := os.Create(*out)
	if err != nil {
		panic(err)
	}
	defer f.Close()
	fmt.Fprintln(f, "hash\tpath\tsource")
	for _, p := range keys {
		fmt.Fprintf(f, "%s\t%s\t%s\n", stingray.Sum(p).String(), p, found[p])
	}
	fmt.Printf("%d verified new paths written to %s\n", len(keys), *out)
}
