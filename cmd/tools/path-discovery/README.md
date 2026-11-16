# Verified unit path discovery

Run from the repository root. Targets unit resources by default; recovered names must hash to a resource of the selected type. Inferred display labels are not path evidence.

```
go run ./cmd/tools/path-discovery -game "C:/path/to/Helldivers 2/data" -source "C:/candidate/texts" -out work/verified-units.tsv
go run ./cmd/tools/path-discovery -game "C:/path/to/Helldivers 2/data" -generate -scan -out work/generated-units.tsv
```

`-known` selects the existing dictionary; `-type` changes the resource type. Reports record the exact full hash, candidate path, and source or generation rule. Generation tests sibling names and common numbered/suffixed variants from known units. The scan reads decoded Lua resources; it does not decrypt game code. Directory scans stream files in 1 MiB chunks with overlapping boundaries, including large DLLs and existing HellForge dumps. Review reports before adding paths to hashes/cracked.txt and hashes/hashes.txt. No memory writes or live-game injection are performed.

November 14, 2026: upstream 8e4909f adds 124 verified unit paths for the installed 8,019-unit build compared with 21ae4cb. Generation recovered content/env_ship/armory/rifle_13mm (0xd7929ac46498ccdb), raising exact unit path coverage from 2,510 to 2,635. Entity-derived labels are separate from these counts.

Use `-generate -wide -recombine` for broader unit discovery using directories and path segments from all known non-audio paths. This remains bounded to the supplied dictionary; it is not exhaustive reversal of the hash.
