// Package wordcloud generates word clouds from frequencies using native Go.
// It has no tokenizer, filesystem, network, or application dependencies.
package wordcloud

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"math"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

var (
	ErrNoWords = errors.New("wordcloud: no positive word frequencies")
	ErrNoSpace = errors.New("wordcloud: no words fit the canvas")
)

// Options controls layout. Start with DefaultOptions; zero is meaningful for
// Margin, PreferHorizontal, RelativeScaling, Seed, and MaxFontSize.
type Options struct {
	Width, Height                      int
	MaxWords                           int
	MinFontSize, MaxFontSize, FontStep int
	Margin                             int
	PreferHorizontal, RelativeScaling  float64
	Seed                               uint64
	Background                         color.NRGBA
	Palette                            []color.NRGBA
}

// DefaultOptions returns settings for a 600x400 cloud on a white background.
// MaxFontSize=0 estimates the initial size by fitting the two largest words.
func DefaultOptions() Options {
	return Options{Width: 600, Height: 400, MaxWords: 400, MinFontSize: 4,
		FontStep: 1, Margin: 2, PreferHorizontal: .9, RelativeScaling: .5,
		Background: color.NRGBA{255, 255, 255, 255}, Palette: append([]color.NRGBA(nil), viridis[:]...)}
}

// Generator is immutable and safe for concurrent Generate calls. Each call
// owns its random source, font faces, and occupancy map. Callers must not mutate
// frequency maps during Generate. The same inputs and seed reproduce a layout.
type Generator struct {
	font    *opentype.Font
	options Options
}

func New(fontData []byte, options Options) (*Generator, error) {
	if options.Width < 1 || options.Height < 1 || options.Width > 4096 || options.Height > 4096 ||
		options.Width*options.Height > 16_000_000 || options.MaxWords < 1 || options.MaxWords > 10000 ||
		options.MinFontSize < 1 || options.MinFontSize > 4096 || options.MaxFontSize < 0 || options.MaxFontSize > 4096 ||
		(options.MaxFontSize != 0 && options.MaxFontSize < options.MinFontSize) ||
		options.FontStep < 1 || options.FontStep > 4096 || options.Margin < 0 || options.Margin > 4096 ||
		!unitInterval(options.PreferHorizontal) || !unitInterval(options.RelativeScaling) || len(options.Palette) == 0 {
		return nil, errors.New("wordcloud: invalid options (use DefaultOptions as a starting point)")
	}
	// Parse retains the underlying bytes; own a copy to keep Generator immutable.
	f, err := opentype.Parse(append([]byte(nil), fontData...))
	if err != nil {
		return nil, err
	}
	options.Palette = append([]color.NRGBA(nil), options.Palette...)
	return &Generator{f, options}, nil
}

func unitInterval(v float64) bool { return !math.IsNaN(v) && v >= 0 && v <= 1 }

// Placement describes the tight glyph rectangle, including rotated text.
// Rectangles may interlock in whitespace, but painted glyphs never overlap.
type Placement struct {
	Word      string
	Frequency int
	FontSize  int
	Bounds    image.Rectangle
	Vertical  bool
	Color     color.NRGBA
}

type Result struct {
	Image *image.RGBA
	Words []Placement
}

// WritePNG uses lossless fast compression and reuses encoder scratch buffers.
func (r *Result) WritePNG(w io.Writer) error {
	encoder := png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: pngBuffers}
	return encoder.Encode(w, r.Image)
}

type encoderPool struct{ sync.Pool }

func (p *encoderPool) Get() *png.EncoderBuffer {
	if b := p.Pool.Get(); b != nil {
		return b.(*png.EncoderBuffer)
	}
	return new(png.EncoderBuffer)
}
func (p *encoderPool) Put(b *png.EncoderBuffer) { p.Pool.Put(b) }

var pngBuffers = &encoderPool{}

type entry struct {
	word  string
	count int
}

func (g *Generator) Generate(ctx context.Context, frequencies map[string]int) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	entries := make([]entry, 0, len(frequencies))
	for word, count := range frequencies {
		if count > 0 && strings.TrimSpace(word) != "" {
			if len(word) > 16384 {
				return nil, errors.New("wordcloud: word exceeds 16384 bytes")
			}
			entries = append(entries, entry{word, count})
		}
	}
	if len(entries) == 0 {
		return nil, ErrNoWords
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].count == entries[j].count {
			return entries[i].word < entries[j].word
		}
		return entries[i].count > entries[j].count
	})
	entries = entries[:min(len(entries), g.options.MaxWords)]
	rng := rand.New(rand.NewPCG(g.options.Seed, g.options.Seed^0x9e3779b97f4a7c15))
	faces := map[int]font.Face{}
	defer func() {
		for _, face := range faces {
			face.Close()
		}
	}()

	size := g.options.MaxFontSize
	if size == 0 {
		size = g.options.Height
		if len(entries) > 1 {
			probe, err := g.generate(ctx, entries[:2], size, rng, faces, false)
			if err != nil {
				return nil, err
			}
			size = probe.Words[0].FontSize
			if len(probe.Words) > 1 {
				b := probe.Words[1].FontSize
				size = 2 * size * b / (size + b)
			}
		}
	}
	return g.generate(ctx, entries, size, rng, faces, true)
}

func (g *Generator) generate(ctx context.Context, entries []entry, size int, rng *rand.Rand, faces map[int]font.Face, paint bool) (*Result, error) {
	o := g.options
	var canvas *image.RGBA
	if paint {
		canvas = image.NewRGBA(image.Rect(0, 0, o.Width, o.Height))
		draw.Draw(canvas, canvas.Bounds(), image.NewUniform(o.Background), image.Point{}, draw.Src)
	}
	occupancy := newOccupancy(o.Width, o.Height)
	result := &Result{Image: canvas}
	last := float64(entries[0].count)
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		size = int(math.Round(float64(size) * (o.RelativeScaling*float64(e.count)/last + 1 - o.RelativeScaling)))
		vertical := rng.Float64() >= o.PreferHorizontal
		triedRotation := false
		var face font.Face
		var bounds image.Rectangle
		var position image.Point
		for size >= o.MinFontSize {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			face = faces[size]
			if face == nil {
				var err error
				face, err = opentype.NewFace(g.font, &opentype.FaceOptions{Size: float64(size), DPI: 72, Hinting: font.HintingNone})
				if err != nil {
					return nil, err
				}
				faces[size] = face
			}
			b, _ := font.BoundString(face, e.word)
			bounds = image.Rect(b.Min.X.Floor(), b.Min.Y.Floor(), b.Max.X.Ceil(), b.Max.Y.Ceil())
			if bounds.Empty() {
				break
			}
			w, h := bounds.Dx(), bounds.Dy()
			if vertical {
				w, h = h, w
			}
			p, ok, err := occupancy.sample(ctx, w+o.Margin, h+o.Margin, rng)
			if err != nil {
				return nil, err
			}
			if ok {
				position = p.Add(image.Pt(o.Margin/2, o.Margin/2))
				break
			}
			if !triedRotation && o.PreferHorizontal < 1 {
				vertical = !vertical
				triedRotation = true
			} else {
				size -= o.FontStep
				vertical = false
			}
		}
		if size < o.MinFontSize {
			break
		}
		if bounds.Empty() {
			continue
		}
		sprite := image.NewAlpha(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
		d := font.Drawer{Dst: sprite, Src: image.White, Face: face, Dot: fixed.P(-bounds.Min.X, -bounds.Min.Y)}
		d.DrawString(e.word)
		if vertical {
			sprite = rotate(sprite)
		}
		c := o.Palette[rng.IntN(len(o.Palette))]
		rect := sprite.Bounds().Add(position)
		if paint {
			draw.DrawMask(canvas, rect, image.NewUniform(c), image.Point{}, sprite, image.Point{}, draw.Over)
		}
		occupancy.add(sprite, position)
		result.Words = append(result.Words, Placement{e.word, e.count, size, rect, vertical, c})
		last = float64(e.count)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(result.Words) == 0 {
		return nil, ErrNoSpace
	}
	return result, nil
}

func rotate(src *image.Alpha) *image.Alpha {
	w, h := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewAlpha(image.Rect(0, 0, h, w))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			dst.Pix[(w-1-x)*dst.Stride+y] = src.Pix[y*src.Stride+x]
		}
	}
	return dst
}

var viridis = [...]color.NRGBA{
	{68, 1, 84, 255}, {72, 36, 117, 255}, {65, 68, 135, 255}, {53, 95, 141, 255},
	{42, 120, 142, 255}, {33, 145, 140, 255}, {34, 168, 132, 255}, {68, 191, 112, 255},
	{122, 209, 81, 255}, {189, 223, 38, 255}, {253, 231, 37, 255},
}
