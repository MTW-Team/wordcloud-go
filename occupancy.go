package wordcloud

import (
	"context"
	"image"
	"math/bits"
	"math/rand/v2"
)

// Each bit records one painted pixel. Rectangle queries and horizontal gap
// scans operate on 64 pixels at once, without per-word canvas-sized updates.
type occupancy struct {
	w, h, stride int
	rows         []uint64
	scratch      []uint64
	tree         []uint64
	base         int
}

func newOccupancy(w, h int) *occupancy {
	stride := (w + 63) / 64
	base := 1
	for base < h {
		base *= 2
	}
	tree := make([]uint64, 2*base*stride)
	return &occupancy{w: w, h: h, stride: stride, rows: tree[base*stride : (base+h)*stride], scratch: make([]uint64, stride), tree: tree, base: base}
}
func (o *occupancy) clearRect(x, y, w, h int) bool {
	first, last := x/64, (x+w-1)/64
	left, right := ^uint64(0)<<uint(x%64), ^uint64(0)>>uint(63-(x+w-1)%64)
	for row := y; row < y+h; row++ {
		p := o.rows[row*o.stride : (row+1)*o.stride]
		if first == last {
			if p[first]&left&right != 0 {
				return false
			}
			continue
		}
		if p[first]&left != 0 || p[last]&right != 0 {
			return false
		}
		for i := first + 1; i < last; i++ {
			if p[i] != 0 {
				return false
			}
		}
	}
	return true
}

// Uniform rejection sampling is cheap when space is plentiful. An exact
// fallback counts all valid starts in horizontal free runs, then selects one.
// It never shrinks a word merely because random probes missed a narrow gap.
func (o *occupancy) sample(ctx context.Context, w, h int, rng *rand.Rand) (image.Point, bool, error) {
	if err := ctx.Err(); err != nil {
		return image.Point{}, false, err
	}
	if w > o.w || h > o.h {
		return image.Point{}, false, nil
	}
	for range 128 {
		x, y := rng.IntN(o.w-w+1), rng.IntN(o.h-h+1)
		if o.clearRect(x, y, w, h) {
			return image.Pt(x, y), true, nil
		}
	}
	count := 0
	for pass := 0; pass < 2; pass++ {
		for y := 0; y <= o.h-h; y++ {
			if y%16 == 0 {
				if err := ctx.Err(); err != nil {
					return image.Point{}, false, err
				}
			}
			clear(o.scratch)
			// A segment tree combines a vertical band in O(log height)
			// bitset unions instead of scanning every glyph row.
			for lo, hi := o.base+y, o.base+y+h; lo < hi; lo, hi = lo/2, hi/2 {
				if lo&1 != 0 {
					o.merge(lo)
					lo++
				}
				if hi&1 != 0 {
					hi--
					o.merge(hi)
				}
			}
			start, x := 0, 0
			for x < o.w {
				v := o.scratch[x/64] >> uint(x%64)
				if v&1 != 0 {
					x += min(bits.TrailingZeros64(^v), 64-x%64, o.w-x)
					start = x
				} else {
					x += min(bits.TrailingZeros64(v), 64-x%64, o.w-x)
					if x < o.w && o.scratch[x/64]>>uint(x%64)&1 == 0 {
						continue
					}
					n := x - start - w + 1
					if n > 0 {
						if pass == 0 {
							count += n
						} else {
							if count < n {
								return image.Pt(start+count, y), true, nil
							}
							count -= n
						}
					}
				}
			}
		}
		if pass == 0 {
			if count == 0 {
				return image.Point{}, false, nil
			}
			count = rng.IntN(count)
		}
	}
	return image.Point{}, false, nil
}
func (o *occupancy) add(sprite *image.Alpha, p image.Point) {
	for y := 0; y < sprite.Rect.Dy(); y++ {
		row := o.rows[(p.Y+y)*o.stride : (p.Y+y+1)*o.stride]
		for x := 0; x < sprite.Rect.Dx(); x++ {
			if sprite.Pix[y*sprite.Stride+x] != 0 {
				xx := p.X + x
				row[xx/64] |= uint64(1) << uint(xx%64)
			}
		}
	}
	// Rebuild just the affected range at each level, once per node.
	for lo, hi := (o.base+p.Y)/2, (o.base+p.Y+sprite.Rect.Dy()-1)/2; lo > 0; lo, hi = lo/2, hi/2 {
		for node := lo; node <= hi; node++ {
			dst := o.tree[node*o.stride : (node+1)*o.stride]
			left := o.tree[2*node*o.stride : (2*node+1)*o.stride]
			right := o.tree[(2*node+1)*o.stride : (2*node+2)*o.stride]
			for i := range dst {
				dst[i] = left[i] | right[i]
			}
		}
	}

}

func (o *occupancy) merge(node int) {
	for i, v := range o.tree[node*o.stride : (node+1)*o.stride] {
		o.scratch[i] |= v
	}
}
