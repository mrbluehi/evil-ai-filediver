package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/xypwn/filediver/stingray"
	stingray_wwise "github.com/xypwn/filediver/stingray/wwise"
	"github.com/xypwn/filediver/wwise"
)

//go:embed data/subtitle_audio_links.json
var subtitleAudioLinksJSON []byte

type subtitleAudioReferenceData struct {
	Subtitles map[string]subtitleAudioReferenceEntry `json:"subtitles"`
}

type subtitleAudioReferenceEntry struct {
	Sounds  []subtitleAudioReferenceLink `json:"sounds"`
	Sources []subtitleAudioReferenceLink `json:"sources"`
}

type subtitleAudioReferenceLink struct {
	ID      uint32 `json:"id"`
	Certain bool   `json:"certain"`
}

type subtitleAudioCandidate struct {
	FileID           stingray.FileID
	SourceID         uint32
	ObjectID         uint32
	Language         string
	BankPath         string
	Certain          bool
	LanguageFallback bool
}

type indexedWwiseSound struct {
	ObjectID  uint32
	SourceID  uint32
	HasSource bool
}

type indexedWwiseBank struct {
	Language string
	Tail     string
	Path     string
	Sounds   []indexedWwiseSound
}

var (
	subtitleAudioReferenceOnce sync.Once
	subtitleAudioReferenceMap  map[uint32]subtitleAudioReferenceEntry
	subtitleAudioReferenceErr  error
)

// lookupSubtitleAudioReference returns Audiodiver's transcript-derived relation from
// one game subtitle ID to its English Wwise Sound objects and standalone clips.
func lookupSubtitleAudioReference(id uint32) (subtitleAudioReferenceEntry, bool, error) {
	subtitleAudioReferenceOnce.Do(func() {
		var raw subtitleAudioReferenceData
		if err := json.Unmarshal(subtitleAudioLinksJSON, &raw); err != nil {
			subtitleAudioReferenceErr = err
			return
		}
		subtitleAudioReferenceMap = make(map[uint32]subtitleAudioReferenceEntry, len(raw.Subtitles))
		for key, link := range raw.Subtitles {
			n, err := strconv.ParseUint(key, 10, 32)
			if err != nil {
				subtitleAudioReferenceErr = fmt.Errorf("invalid caption ID %q: %w", key, err)
				return
			}
			subtitleAudioReferenceMap[uint32(n)] = link
		}
	})
	if subtitleAudioReferenceErr != nil {
		return subtitleAudioReferenceEntry{}, false, subtitleAudioReferenceErr
	}
	link, ok := subtitleAudioReferenceMap[id]
	return link, ok, nil
}

func buildSubtitleAudioIndex(gd *GameData, reportProgress func(float32)) (map[uint32][]subtitleAudioCandidate, error) {
	if gd == nil || gd.DataDir == nil {
		return nil, fmt.Errorf("no active game data")
	}
	if _, _, err := lookupSubtitleAudioReference(0); err != nil {
		return nil, err
	}

	objectLinks := make(map[uint32][]struct {
		subtitle uint32
		certain  bool
	})
	sourceLinks := make(map[uint32][]struct {
		subtitle uint32
		certain  bool
	})
	for subtitleTextID := range subtitleAudioReferenceMap {
		link := subtitleAudioReferenceMap[subtitleTextID]
		for _, sound := range link.Sounds {
			objectLinks[sound.ID] = append(objectLinks[sound.ID], struct {
				subtitle uint32
				certain  bool
			}{subtitleTextID, sound.Certain})
		}
		for _, source := range link.Sources {
			sourceLinks[source.ID] = append(sourceLinks[source.ID], struct {
				subtitle uint32
				certain  bool
			}{subtitleTextID, source.Certain})
		}
	}

	bankIDs := make([]stingray.FileID, 0)
	for id := range gd.DataDir.Files {
		if id.Type == stingray.Sum("wwise_bank") {
			bankIDs = append(bankIDs, id)
		}
	}
	slices.SortFunc(bankIDs, stingray.FileID.Cmp)
	if len(bankIDs) == 0 {
		return nil, fmt.Errorf("this build contains no Wwise banks")
	}

	banksByLanguageAndTail := make(map[string]map[string][]indexedWwiseBank)
	parsedBanks := 0
	for i, id := range bankIDs {
		bankPath := gd.LookupHash(id.Name)
		if bankPath == id.Name.String() || bankPath == "" {
			continue
		}
		bankPath = strings.ReplaceAll(bankPath, "\\", "/")
		bankPath = strings.TrimSuffix(bankPath, ".wwise_bank")
		language, tail, ok := audioBankLanguageAndTail(bankPath)
		if !ok {
			continue
		}
		data, err := gd.DataDir.Read(id, stingray.DataMain)
		if err != nil {
			continue
		}
		bank, err := stingray_wwise.OpenBnk(bytes.NewReader(data))
		if err != nil {
			continue
		}
		sounds := make([]indexedWwiseSound, 0)
		for _, object := range bank.HircObjects {
			if object.Header.Type != wwise.BnkHircObjectSound {
				continue
			}
			sounds = append(sounds, indexedWwiseSound{
				ObjectID:  object.Header.ObjectID,
				SourceID:  object.Sound.SourceID,
				HasSource: object.Sound.SourceBits > 0 && object.Sound.SourceID != 0,
			})
		}
		banksByLanguageAndTail[language] = ensureBankTailMap(banksByLanguageAndTail[language])
		banksByLanguageAndTail[language][tail] = append(banksByLanguageAndTail[language][tail], indexedWwiseBank{
			Language: language,
			Tail:     tail,
			Path:     bankPath,
			Sounds:   sounds,
		})
		parsedBanks++
		if reportProgress != nil && (i%24 == 0 || i == len(bankIDs)-1) {
			reportProgress(float32(i+1) / float32(len(bankIDs)))
		}
	}
	if parsedBanks == 0 {
		return nil, fmt.Errorf("no readable, language-scoped Wwise banks were found")
	}

	// The reference maps English Sound IDs. Other language banks pair to the
	// English bank with the same path tail and the same ordered Sound layout.
	// Requiring an equal count prevents a shifted/renumbered bank from linking a
	// caption to a neighboring line.
	index := make(map[uint32][]subtitleAudioCandidate)
	seen := make(map[uint32]map[stingray.FileID]int)
	add := func(subtitle uint32, candidate subtitleAudioCandidate) {
		if seen[subtitle] == nil {
			seen[subtitle] = make(map[stingray.FileID]int)
		}
		if at, ok := seen[subtitle][candidate.FileID]; ok {
			if candidate.Certain && !index[subtitle][at].Certain {
				index[subtitle][at] = candidate
			}
			return
		}
		seen[subtitle][candidate.FileID] = len(index[subtitle])
		index[subtitle] = append(index[subtitle], candidate)
	}

	for language, byTail := range banksByLanguageAndTail {
		for tail, targetBanks := range byTail {
			englishBanks := banksByLanguageAndTail["us"][tail]
			for _, targetBank := range targetBanks {
				englishBank := targetBank
				if language != "us" {
					if len(englishBanks) == 0 {
						continue
					}
					englishBank = englishBanks[0]
					if len(englishBank.Sounds) != len(targetBank.Sounds) {
						continue
					}
				}
				for ordinal, sound := range englishBank.Sounds {
					matches := objectLinks[sound.ObjectID]
					if len(matches) == 0 || ordinal >= len(targetBank.Sounds) {
						continue
					}
					targetSound := targetBank.Sounds[ordinal]
					if !targetSound.HasSource {
						continue
					}
					fileID, ok := audioStreamForBankSource(gd, targetBank.Path, targetSound.SourceID)
					if !ok {
						continue
					}
					for _, match := range matches {
						add(match.subtitle, subtitleAudioCandidate{
							FileID:   fileID,
							SourceID: targetSound.SourceID,
							ObjectID: targetSound.ObjectID,
							Language: language,
							BankPath: targetBank.Path,
							Certain:  match.certain,
						})
					}
				}
			}
		}
	}

	// A handful of voice clips have no HIRC Sound object. Audiodiver indexes
	// those directly by the language-specific stream source ID.
	for sourceID, matches := range sourceLinks {
		streamPath := path.Join("content", "audio", "us", strconv.FormatUint(uint64(sourceID), 10))
		fileID := stingray.FileID{Name: stingray.Sum(streamPath), Type: stingray.Sum("wwise_stream")}
		if _, ok := gd.DataDir.Files[fileID]; !ok {
			continue
		}
		for _, match := range matches {
			add(match.subtitle, subtitleAudioCandidate{
				FileID: fileID, SourceID: sourceID, Language: "us", Certain: match.certain,
			})
		}
	}

	for subtitle := range index {
		sort.SliceStable(index[subtitle], func(i, j int) bool {
			left, right := index[subtitle][i], index[subtitle][j]
			if left.Certain != right.Certain {
				return left.Certain
			}
			if left.Language != right.Language {
				return left.Language < right.Language
			}
			if left.BankPath != right.BankPath {
				return left.BankPath < right.BankPath
			}
			return left.SourceID < right.SourceID
		})
	}
	if reportProgress != nil {
		reportProgress(1)
	}
	return index, nil
}

func ensureBankTailMap(v map[string][]indexedWwiseBank) map[string][]indexedWwiseBank {
	if v == nil {
		return make(map[string][]indexedWwiseBank)
	}
	return v
}

func audioBankLanguageAndTail(bankPath string) (language, tail string, ok bool) {
	parts := strings.Split(strings.ToLower(strings.Trim(bankPath, "/")), "/")
	if len(parts) < 4 || parts[0] != "content" || parts[1] != "audio" {
		return "", "", false
	}
	language = parts[2]
	switch language {
	case "us", "gb", "bp", "cn", "de", "es", "fr", "it", "jp", "ko", "ms", "nl", "pl", "pt", "ru", "sc", "tc":
	default:
		return "", "", false
	}
	return language, strings.Join(parts[3:], "/"), true
}

func audioStreamForBankSource(gd *GameData, bankPath string, sourceID uint32) (stingray.FileID, bool) {
	resourcePath := path.Join(path.Dir(bankPath), strconv.FormatUint(uint64(sourceID), 10))
	id := stingray.FileID{Name: stingray.Sum(resourcePath), Type: stingray.Sum("wwise_stream")}
	_, ok := gd.DataDir.Files[id]
	return id, ok
}

func subtitleAudioLanguageCode(language string) string {
	switch language {
	case "English (US)":
		return "us"
	case "English (UK)":
		return "gb"
	case "Portuguese (Brazil)":
		return "bp"
	case "Portuguese (Europe)":
		return "pt"
	case "Spanish (Spain)":
		return "es"
	case "Spanish (Mexico)":
		return "ms"
	case "Chinese (Simplified)":
		return "cn"
	case "Chinese (Traditional)":
		return "tc"
	case "German":
		return "de"
	case "French":
		return "fr"
	case "Italian":
		return "it"
	case "Japanese":
		return "jp"
	case "Korean":
		return "ko"
	case "Dutch":
		return "nl"
	case "Polish":
		return "pl"
	case "Russian":
		return "ru"
	default:
		return ""
	}
}

func relatedAudioVariants(index map[uint32][]subtitleAudioCandidate, row StringSearchResult) []subtitleAudioCandidate {
	subtitleID, err := strconv.ParseUint(row.ID, 10, 32)
	if err != nil {
		return nil
	}
	return relatedAudioVariantsFromList(index[uint32(subtitleID)], row.Language)
}

func relatedAudioVariantsFromList(candidates []subtitleAudioCandidate, language string) []subtitleAudioCandidate {
	code := subtitleAudioLanguageCode(language)
	selected := make([]subtitleAudioCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if code == "" || candidate.Language == code {
			selected = append(selected, candidate)
		}
	}
	fallback := false
	if len(selected) == 0 && code != "" && code != "us" {
		fallback = true
		for _, candidate := range candidates {
			if candidate.Language == "us" {
				selected = append(selected, candidate)
			}
		}
	}
	for i := range selected {
		selected[i].LanguageFallback = fallback
	}
	sort.SliceStable(selected, func(i, j int) bool {
		if selected[i].Certain != selected[j].Certain {
			return selected[i].Certain
		}
		if selected[i].BankPath != selected[j].BankPath {
			return selected[i].BankPath < selected[j].BankPath
		}
		if selected[i].Language != selected[j].Language {
			return selected[i].Language < selected[j].Language
		}
		return selected[i].SourceID < selected[j].SourceID
	})
	return selected
}

func relatedAudioVariantLabel(candidate subtitleAudioCandidate) string {
	bank := "standalone voice clip"
	if candidate.BankPath != "" {
		bank = path.Base(candidate.BankPath)
	}
	label := fmt.Sprintf("%s · source %d", bank, candidate.SourceID)
	if !candidate.Certain {
		label += " · uncertain duplicate-caption match"
	}
	if candidate.LanguageFallback {
		label += " · English (US) fallback"
	}
	return label
}
