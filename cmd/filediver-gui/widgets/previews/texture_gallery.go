package previews

import (
	"bytes"
	"image"
	"io"
	"sync"

	"github.com/AllenDang/cimgui-go/imgui"
	"github.com/go-gl/gl/v4.3-core/gl"
	"github.com/xypwn/filediver/dds"
	"github.com/xypwn/filediver/stingray"
	"github.com/xypwn/filediver/stingray/unit/texture"
)

const textureThumbnailCacheLimit = 80

type textureThumbJob struct {
	id         stingray.FileID
	generation uint64
	maxDim     int
}
type textureThumbResult struct {
	id         stingray.FileID
	generation uint64
	image      image.Image
	failed     bool
}
type textureThumbCacheEntry struct {
	image    *ImagePreviewImage
	lastUsed uint64
}

// TextureGallery decodes DDS data off the UI thread and uploads only small previews on it.
type TextureGallery struct {
	getResource GetResourceFunc
	jobs        chan textureThumbJob
	results     chan textureThumbResult
	stop        chan struct{}
	stopOnce    sync.Once
	images      map[stingray.FileID]textureThumbCacheEntry
	pending     map[stingray.FileID]uint64
	failed      map[stingray.FileID]bool
	maxDim      int
	nearest     bool
	generation  uint64
	frame       uint64
}

func NewTextureGallery(getResource GetResourceFunc) *TextureGallery {
	g := &TextureGallery{getResource: getResource, jobs: make(chan textureThumbJob, 2), results: make(chan textureThumbResult, 8), stop: make(chan struct{}), images: make(map[stingray.FileID]textureThumbCacheEntry), pending: make(map[stingray.FileID]uint64), failed: make(map[stingray.FileID]bool), maxDim: 512}
	go g.decodeWorker()
	return g
}

func (g *TextureGallery) decodeWorker() {
	for {
		select {
		case <-g.stop:
			return
		case job := <-g.jobs:
			img, ok := g.decode(job.id, job.maxDim)
			result := textureThumbResult{id: job.id, generation: job.generation, image: img, failed: !ok}
			select {
			case g.results <- result:
			case <-g.stop:
				return
			}
		}
	}
}

func (g *TextureGallery) Delete() {
	g.stopOnce.Do(func() { close(g.stop) })
	g.clearCache()
}

func (g *TextureGallery) clearCache() {
	for _, entry := range g.images {
		if entry.image.textureId != 0 {
			gl.DeleteTextures(1, &entry.image.textureId)
		}
	}
	clear(g.images)
	clear(g.failed)
	clear(g.pending)
}

func (g *TextureGallery) BeginFrame(maxDim int, nearest bool) {
	if maxDim != g.maxDim || nearest != g.nearest {
		g.generation++
		g.maxDim, g.nearest = maxDim, nearest
		g.clearCache()
	}
	g.frame++
	// Keep GL uploads bounded per frame; CPU decode runs on a single worker.
	for range 2 {
		select {
		case result := <-g.results:
			delete(g.pending, result.id)
			if result.generation != g.generation {
				continue
			}
			if result.failed || result.image == nil {
				g.failed[result.id] = true
				continue
			}
			pv := NewImagePreview()
			pv.addImage(result.image)
			if len(pv.Images) == 0 || pv.Images[0].textureId == 0 {
				g.failed[result.id] = true
				continue
			}
			thumbnail := pv.Images[0]
			if g.nearest {
				gl.BindTexture(gl.TEXTURE_2D, thumbnail.textureId)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MIN_FILTER, gl.NEAREST)
				gl.TexParameteri(gl.TEXTURE_2D, gl.TEXTURE_MAG_FILTER, gl.NEAREST)
				gl.BindTexture(gl.TEXTURE_2D, 0)
			}
			g.images[result.id] = textureThumbCacheEntry{image: thumbnail, lastUsed: g.frame}
			g.evictOldest()
		default:
			return
		}
	}
}

func (g *TextureGallery) evictOldest() {
	for len(g.images) > textureThumbnailCacheLimit {
		var oldestID stingray.FileID
		oldestFrame := ^uint64(0)
		for id, entry := range g.images {
			if entry.lastUsed < oldestFrame {
				oldestID, oldestFrame = id, entry.lastUsed
			}
		}
		entry := g.images[oldestID]
		gl.DeleteTextures(1, &entry.image.textureId)
		delete(g.images, oldestID)
	}
}

func (g *TextureGallery) DrawThumbnail(id stingray.FileID, pos, size imgui.Vec2) bool {
	entry, ok := g.images[id]
	if ok {
		entry.lastUsed = g.frame
		g.images[id] = entry
		s := entry.image.size
		if s.X > 0 && s.Y > 0 {
			fit := min(size.X/s.X, size.Y/s.Y)
			drawSize := s.Mul(fit)
			drawPos := pos.Add(size.Sub(drawSize).Mul(0.5))
			// Match ImagePreview's default UV orientation so gallery cards and the
			// full texture preview display decoded top-left-origin pixels alike.
			imgui.WindowDrawList().AddImage(entry.image.textureRef, drawPos, drawPos.Add(drawSize))
		}
	} else {
		imgui.WindowDrawList().AddRectFilled(pos, pos.Add(size), imgui.ColorU32Vec4(imgui.NewVec4(0.12, 0.15, 0.2, 1)))
		if !g.failed[id] {
			if _, exists := g.pending[id]; !exists {
				g.pending[id] = g.generation
				select {
				case g.jobs <- textureThumbJob{id: id, generation: g.generation, maxDim: g.maxDim}:
				default:
					delete(g.pending, id)
				}
			}
		}
	}
	imgui.SetCursorScreenPos(pos)
	return imgui.InvisibleButton("##texture-image", size)
}

func (g *TextureGallery) decode(id stingray.FileID, maxDim int) (image.Image, bool) {
	parts := make([][]byte, 3)
	for _, typ := range []stingray.DataType{stingray.DataMain, stingray.DataStream, stingray.DataGPU} {
		data, exists, err := g.getResource(id, typ)
		if err != nil {
			return nil, false
		}
		if exists {
			parts[typ] = data
		}
	}
	var decoded *dds.DDS
	if len(parts[stingray.DataMain]) >= 4 && string(parts[stingray.DataMain][:4]) == "DDS " {
		decoded, _ = dds.Decode(bytes.NewReader(bytes.Join(parts, nil)), false)
	} else {
		r := io.MultiReader(bytes.NewReader(parts[0]), bytes.NewReader(parts[1]), bytes.NewReader(parts[2]))
		if _, err := texture.DecodeInfo(r); err != nil {
			return nil, false
		}
		decoded, _ = dds.Decode(r, false)
	}
	if decoded == nil || decoded.Image == nil {
		return nil, false
	}
	return downsampleTextureImage(decoded.Image, maxDim), true
}

func downsampleTextureImage(source image.Image, maxDim int) image.Image {
	b := source.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	if maxDim <= 0 {
		maxDim = 512
	}
	scale := min(float64(maxDim)/float64(w), float64(maxDim)/float64(h), 1.0)
	dw, dh := max(1, int(float64(w)*scale)), max(1, int(float64(h)*scale))
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < dh; y++ {
		sy := b.Min.Y + y*h/dh
		for x := 0; x < dw; x++ {
			sx := b.Min.X + x*w/dw
			dst.Set(x, y, source.At(sx, sy))
		}
	}
	return dst
}
