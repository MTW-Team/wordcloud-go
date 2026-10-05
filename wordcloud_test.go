package wordcloud

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"

	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

func testGenerator(t testing.TB, o Options) *Generator {
	t.Helper()
	g, err := New(goregular.TTF, o)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func fixture(n int) map[string]int {
	m := make(map[string]int, n)
	for i := 0; i < n; i++ {
		m[fmt.Sprintf("station%03d", i)] = 1 + n/(i+1)
	}
	return m
}

func TestDeterministicConcurrentAndPNG(t *testing.T) {
	g := testGenerator(t, DefaultOptions())
	frequencies := fixture(60)
	want, err := g.Generate(t.Context(), frequencies)
	if err != nil {
		t.Fatal(err)
	}
	if len(want.Words) < 30 {
		t.Fatalf("only %d words placed", len(want.Words))
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			got, err := g.Generate(t.Context(), frequencies)
			if err != nil {
				t.Error(err)
				return
			}
			if err := got.WritePNG(io.Discard); err != nil {
				t.Error(err)
			}
			if !reflect.DeepEqual(got.Words, want.Words) || !bytes.Equal(got.Image.Pix, want.Image.Pix) {
				t.Error("non-deterministic output")
			}
		})
	}
	wg.Wait()
	var b bytes.Buffer
	if err := want.WritePNG(&b); err != nil {
		t.Fatal(err)
	}
	decoded, err := png.Decode(&b)
	if err != nil || decoded.Bounds() != image.Rect(0, 0, 600, 400) {
		t.Fatal("invalid PNG", err)
	}
	if err := want.WritePNG(failingWriter{}); err == nil {
		t.Fatal("lost encoder error")
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestGlyphsNeverOverlapOrClip(t *testing.T) {
	o := DefaultOptions()
	o.PreferHorizontal = .5
	g := testGenerator(t, o)
	result, err := g.Generate(t.Context(), fixture(150))
	if err != nil {
		t.Fatal(err)
	}
	occupied := image.NewAlpha(result.Image.Bounds())
	vertical := 0
	lastSize := 4096
	for _, p := range result.Words {
		if !p.Bounds.In(result.Image.Bounds()) {
			t.Fatalf("clipped: %+v", p)
		}
		if p.FontSize > lastSize {
			t.Fatal("font sizes increased")
		}
		lastSize = p.FontSize
		face, err := opentype.NewFace(g.font, &opentype.FaceOptions{Size: float64(p.FontSize), DPI: 72})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := font.BoundString(face, p.Word)
		sprite := image.NewAlpha(image.Rect(0, 0, b.Max.X.Ceil()-b.Min.X.Floor(), b.Max.Y.Ceil()-b.Min.Y.Floor()))
		d := font.Drawer{Dst: sprite, Src: image.White, Face: face, Dot: fixed.P(-b.Min.X.Floor(), -b.Min.Y.Floor())}
		d.DrawString(p.Word)
		face.Close()
		if p.Vertical {
			sprite = rotate(sprite)
			vertical++
		}
		if sprite.Bounds().Size() != p.Bounds.Size() {
			t.Fatal("wrong glyph bounds")
		}
		for y := 0; y < sprite.Rect.Dy(); y++ {
			for x := 0; x < sprite.Rect.Dx(); x++ {
				if sprite.AlphaAt(x, y).A == 0 {
					continue
				}
				px, py := p.Bounds.Min.X+x, p.Bounds.Min.Y+y
				if occupied.AlphaAt(px, py).A != 0 {
					t.Fatalf("overlap at %d,%d", px, py)
				}
				occupied.SetAlpha(px, py, color.Alpha{255})
			}
		}
	}
	if vertical == 0 || vertical == len(result.Words) {
		t.Fatal("missing mixed orientations")
	}
}

func TestOptionsEmptyAndCancellation(t *testing.T) {
	for _, change := range []func(*Options){
		func(o *Options) { o.Width = 0 }, func(o *Options) { o.Height = 5000 },
		func(o *Options) { o.FontStep = 0 }, func(o *Options) { o.MinFontSize = 0 },
		func(o *Options) { o.RelativeScaling = math.NaN() }, func(o *Options) { o.PreferHorizontal = 2 },
		func(o *Options) { o.Palette = nil }, func(o *Options) { o.Margin = -1 },
	} {
		o := DefaultOptions()
		change(&o)
		if _, err := New(goregular.TTF, o); err == nil {
			t.Fatal("invalid options accepted")
		}
	}
	if _, err := New([]byte("bad font"), DefaultOptions()); err == nil {
		t.Fatal("bad font accepted")
	}
	g := testGenerator(t, DefaultOptions())
	if _, err := g.Generate(t.Context(), map[string]int{"a": 0, "b": -1, " ": 1}); !errors.Is(err, ErrNoWords) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := g.Generate(ctx, fixture(10)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	o := DefaultOptions()
	o.Width = 1
	o.Height = 1
	if _, err := testGenerator(t, o).Generate(t.Context(), map[string]int{"long": 1}); !errors.Is(err, ErrNoSpace) {
		t.Fatal(err)
	}
	o = DefaultOptions()
	o.MaxWords = 1
	o.PreferHorizontal = 1
	r, err := testGenerator(t, o).Generate(t.Context(), fixture(10))
	if err != nil || len(r.Words) != 1 || r.Words[0].Vertical {
		t.Fatal(r, err)
	}
}

// Check rectangle queries against a pixel oracle across 64-bit boundaries.
func TestOccupancyAgainstPixels(t *testing.T) {
	o := newOccupancy(137, 23)
	oracle := image.NewAlpha(image.Rect(0, 0, o.w, o.h))
	rng := rand.New(rand.NewPCG(1, 2))
	for range 20 {
		sprite := image.NewAlpha(image.Rect(0, 0, 3, 2))
		sprite.SetAlpha(0, 0, color.Alpha{1})
		sprite.SetAlpha(2, 1, color.Alpha{255})
		p := image.Pt(rng.IntN(o.w-2), rng.IntN(o.h-1))
		o.add(sprite, p)
		oracle.SetAlpha(p.X, p.Y, color.Alpha{255})
		oracle.SetAlpha(p.X+2, p.Y+1, color.Alpha{255})
	}
	for range 1000 {
		w, h := 1+rng.IntN(o.w), 1+rng.IntN(o.h)
		x, y := rng.IntN(o.w-w+1), rng.IntN(o.h-h+1)
		want := true
		for yy := y; yy < y+h; yy++ {
			for xx := x; xx < x+w; xx++ {
				if oracle.AlphaAt(xx, yy).A != 0 {
					want = false
				}
			}
		}
		if got := o.clearRect(x, y, w, h); got != want {
			t.Fatalf("rectangle %d,%d %dx%d: got %v want %v", x, y, w, h, got, want)
		}
		p, ok, err := o.sample(t.Context(), w, h, rng)
		if err != nil {
			t.Fatal(err)
		}
		exists := false
		for yy := 0; yy <= o.h-h; yy++ {
			for xx := 0; xx <= o.w-w; xx++ {
				if o.clearRect(xx, yy, w, h) {
					exists = true
				}
			}
		}
		if ok != exists || (ok && !o.clearRect(p.X, p.Y, w, h)) {
			t.Fatal("search missed space or found overlap", w, h, p, ok)
		}
	}
	empty := newOccupancy(3, 2)
	p, ok, err := empty.sample(t.Context(), 3, 2, rng)
	if err != nil || !ok || p != (image.Point{}) {
		t.Fatal("exact fit lost", p, ok, err)
	}
	sprite := image.NewAlpha(image.Rect(0, 0, 3, 2))
	for i := range sprite.Pix {
		sprite.Pix[i] = 255
	}
	empty.add(sprite, image.Point{})
	if _, ok, _ := empty.sample(t.Context(), 1, 1, rng); ok {
		t.Fatal("occupied pixel selected")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, _, err := o.sample(ctx, 1, 1, rng); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func BenchmarkGenerate(b *testing.B) {
	for _, n := range []int{50, 200, 400} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			g := testGenerator(b, DefaultOptions())
			frequencies := fixture(n)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				if _, err := g.Generate(b.Context(), frequencies); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func TestSingleGapAtBitBoundary(t *testing.T) {
	o := newOccupancy(600, 400)
	sprite := image.NewAlpha(image.Rect(0, 0, 600, 400))
	for i := range sprite.Pix {
		sprite.Pix[i] = 255
	}
	// Exactly one fitting rectangle, crossing the 64-bit word boundary.
	want := image.Rect(63, 311, 66, 313)
	for y := want.Min.Y; y < want.Max.Y; y++ {
		for x := want.Min.X; x < want.Max.X; x++ {
			sprite.SetAlpha(x, y, color.Alpha{})
		}
	}
	o.add(sprite, image.Point{})
	for seed := range uint64(5) {
		p, ok, err := o.sample(t.Context(), 3, 2, rand.New(rand.NewPCG(seed, 10)))
		if err != nil || !ok || p != want.Min {
			t.Fatalf("missed isolated gap: %v, %v, %v", p, ok, err)
		}
	}
}

func BenchmarkPNG(b *testing.B) {
	g := testGenerator(b, DefaultOptions())
	r, err := g.Generate(b.Context(), fixture(400))
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		if err := r.WritePNG(io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
