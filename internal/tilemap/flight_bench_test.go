package tilemap

import (
	"context"
	"crypto/sha256"
	"math"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"

	"github.com/wisborg/fitactivity"
	osm "github.com/wisborg/osmbase/render"
	"github.com/wisborg/osmbase/slice"
	"github.com/wisborg/osmbase/terrain"
)

// benchZooms are the zooms of a flyover's pyramid the benchmark draws tiles
// at along the track: a wide view's, two between, and a close one's.
var benchZooms = []uint8{7, 9, 11, 13}

// benchPerZoom is the most tiles drawn at one zoom, taken evenly along the
// track, so a long flight is not ten thousand tiles at its finest.
const benchPerZoom = 160

type benchTile struct {
	z    uint8
	x, y uint32
}

// BenchmarkFlightTiles draws the tiles of a flyover's pyramid along a track
// from the local map store -- with its terrain, as a flyover draws them --
// and reports what one tile costs: time, and memory allocated. It is the
// measure for a change to how a tile is drawn, osmbase's included, and its
// digest of every tile's pixels is what says such a change drew nothing
// differently.
//
// It needs a real store, so it runs only when FD_TILEBENCH_TRACK names the
// track (any file fitdash reads) whose tiles to draw, from the store at
// FD_TILEBENCH_STORE or the default one; the tiles are drawn from what the
// store holds of that ground. scripts/fd tilebench runs it. Each run opens
// the store afresh, so a cache starts cold, and draws on as many goroutines
// as -cpu says.
func BenchmarkFlightTiles(b *testing.B) {
	path := os.Getenv("FD_TILEBENCH_TRACK")
	if path == "" {
		b.Skip("set FD_TILEBENCH_TRACK to a track whose ground the local store holds")
	}
	track, err := fitactivity.Read(path)
	if err != nil {
		b.Fatal(err)
	}
	tiles, region := benchTilesAlong(track)
	if len(tiles) == 0 {
		b.Fatal("the track has no positions")
	}
	root := os.Getenv("FD_TILEBENCH_STORE")
	if root == "" {
		if root, err = slice.DefaultRoot(); err != nil {
			b.Fatal(err)
		}
	}
	var relief *Terrain
	if ts, err := terrain.Open(terrain.Root(root)); err == nil {
		relief = &Terrain{Store: ts}
	}
	workers := runtime.GOMAXPROCS(0)

	var digest [32]byte
	var allocated uint64
	b.ResetTimer()
	for range b.N {
		b.StopTimer()
		l, err := OpenLocal(root, lightInks(), nil, relief)
		if err != nil {
			b.Fatal(err)
		}
		if err := l.UseOutdoors(); err != nil {
			b.Fatal(err)
		}
		o := l.opts
		o.LiftLabels = true
		areas, err := osm.MeasureAreas(context.Background(), l.source, o, region, affordableZoom(slice.Bounds{West: region.West, South: region.South, East: region.East, North: region.North}, 15))
		if err != nil {
			b.Fatal(err)
		}
		var before runtime.MemStats
		runtime.ReadMemStats(&before)
		b.StartTimer()

		sums := make([][32]byte, len(tiles))
		var wg sync.WaitGroup
		next := make(chan int)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for i := range next {
					k := tiles[i]
					tile, err := l.flightTile(context.Background(), k.z, k.x, k.y, areas)
					if err != nil {
						b.Error(err)
						continue
					}
					sums[i] = sha256.Sum256(tile.Image.Pix)
				}
			}()
		}
		for i := range tiles {
			next <- i
		}
		close(next)
		wg.Wait()

		b.StopTimer()
		var after runtime.MemStats
		runtime.ReadMemStats(&after)
		allocated += after.TotalAlloc - before.TotalAlloc
		h := sha256.New()
		for _, s := range sums {
			h.Write(s[:])
		}
		copy(digest[:], h.Sum(nil))
		b.StartTimer()
	}
	b.StopTimer()
	n := float64(b.N * len(tiles))
	b.ReportMetric(float64(b.Elapsed().Milliseconds())/n, "ms/tile")
	b.ReportMetric(float64(allocated)/1e6/n, "MB/tile")
	b.ReportMetric(0, "ns/op")
	b.Logf("%d tiles at zooms %v on %d goroutines; pixels' digest %x", len(tiles), benchZooms, workers, digest[:8])
}

// benchTilesAlong is the tiles under the track's fixes at each of
// benchZooms, at most benchPerZoom of each taken evenly along it, and the
// ground they cover.
func benchTilesAlong(track *fitactivity.Track) ([]benchTile, osm.Bounds) {
	var out []benchTile
	region := osm.Bounds{West: 180, South: 90, East: -180, North: -90}
	for _, z := range benchZooms {
		n := math.Exp2(float64(z))
		seen := map[benchTile]bool{}
		var at []benchTile
		for _, s := range track.Samples {
			if !s.HasGPS {
				continue
			}
			lat := s.Lat * math.Pi / 180
			k := benchTile{z,
				uint32(math.Min(n-1, math.Max(0, (s.Lon+180)/360*n))),
				uint32(math.Min(n-1, math.Max(0, (1-math.Log(math.Tan(lat)+1/math.Cos(lat))/math.Pi)/2*n)))}
			if !seen[k] {
				seen[k] = true
				at = append(at, k)
			}
		}
		step := max(1, (len(at)+benchPerZoom-1)/benchPerZoom)
		for i := 0; i < len(at); i += step {
			k := at[i]
			out = append(out, k)
			west, east := float64(k.x)/n*360-180, float64(k.x+1)/n*360-180
			north := math.Atan(math.Sinh(math.Pi*(1-2*float64(k.y)/n))) * 180 / math.Pi
			south := math.Atan(math.Sinh(math.Pi*(1-2*float64(k.y+1)/n))) * 180 / math.Pi
			region.West, region.East = math.Min(region.West, west), math.Max(region.East, east)
			region.South, region.North = math.Min(region.South, south), math.Max(region.North, north)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].z < out[j].z })
	return out, region
}
