package panel

import (
	"sync"
	"testing"
	"time"
)

// recordProgress collects what Context.BasemapProgress is told, in order.
type recordProgress struct {
	mu    sync.Mutex
	calls [][2]int
}

func (r *recordProgress) report(done, total int) {
	r.mu.Lock()
	r.calls = append(r.calls, [2]int{done, total})
	r.mu.Unlock()
}

func (r *recordProgress) snapshot() [][2]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([][2]int, len(r.calls))
	copy(out, r.calls)
	return out
}

// TestRoutePanel_BasemapProgressCountsEveryViewOnceAndNeverGoesBackwards is
// the contract behind the "map imagery" line.
//
// The count matters twice over. It has to reach its total, or the line sits
// at 3 of 4 while the render carries on without it and reads as something
// that gave up. And it must not go backwards, which is a live risk rather
// than a theoretical one now that the views are fetched concurrently: two
// goroutines incrementing an atomic can still report out of order, and a
// viewer would see a retry that never happened.
func TestRoutePanel_BasemapProgressCountsEveryViewOnceAndNeverGoesBackwards(t *testing.T) {
	const w, h = 800, 800
	const fixes = 1200
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	track := squareTrack(base, fixes)
	highlights := []Highlight{
		{Name: "one", From: 0, To: 200 * time.Second, Zoom: true},
		{Name: "two", From: 300 * time.Second, To: 500 * time.Second, Zoom: true},
		{Name: "unzoomed", From: 600 * time.Second, To: 700 * time.Second},
	}
	ctx := routeHighlightContext(t, track, fixes, highlights, w, h)
	bm := &fakeBasemap{fill: basemapGreen}
	ctx.Basemap = bm
	rec := &recordProgress{}
	ctx.BasemapProgress = rec.report

	RoutePanel{}.Prepare(ctx, Box{X: 40, Y: 40, W: w - 80, H: h - 80})

	calls := rec.snapshot()
	if len(calls) == 0 {
		t.Fatal("BasemapProgress was never called, so a render fetching imagery says nothing while it waits")
	}

	// The whole course plus the two zooming highlights. The third highlight
	// asked for no zoom, so it is not a view and must not be counted -- a
	// total taken from the highlight list rather than from what is actually
	// fetched would stop one short for the whole wait.
	const wantTotal = 3
	if got := bm.calls; got != wantTotal {
		t.Fatalf("fetched %d views, want %d; the fixture is not what this test thinks it is", got, wantTotal)
	}

	if first := calls[0]; first != [2]int{0, wantTotal} {
		t.Errorf("the opening call is %v, want [0 %d]: the count has to arrive before the first request, or the line has nothing to show during the wait it exists for",
			first, wantTotal)
	}
	last := calls[len(calls)-1]
	if last != [2]int{wantTotal, wantTotal} {
		t.Errorf("the closing call is %v, want [%d %d]: the line stops short of its own total and reads as a fetch that gave up",
			last, wantTotal, wantTotal)
	}
	prev := -1
	for i, c := range calls {
		if c[1] != wantTotal {
			t.Errorf("call %d reports a total of %d, want %d throughout", i, c[1], wantTotal)
		}
		if c[0] < prev {
			t.Errorf("call %d reports %d done after %d; the count went backwards", i, c[0], prev)
		}
		prev = c[0]
	}
	if len(calls) != wantTotal+1 {
		t.Errorf("%d calls for %d views; want one opening call and one per view, or a view was counted twice or not at all",
			len(calls), wantTotal)
	}
}

// TestRoutePanel_BasemapProgressCountsOnlyTheViewsActuallyRequested covers
// the case that makes the total unpredictable from the command line: a
// highlight whose span carries no GPS resolves no zoomed view and is never
// fetched.
//
// It is why the count comes from the panel at all rather than being worked
// out in cmd from the highlight list -- which would have named a total the
// fetch could never reach.
func TestRoutePanel_BasemapProgressCountsOnlyTheViewsActuallyRequested(t *testing.T) {
	const w, h = 800, 800
	const fixes = 600
	base := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
	// The GPS locks on halfway through; the highlight sits entirely in the
	// blind stretch before it, so there is no stretch of course to frame.
	track := squareTrackWithLateGPS(base, fixes, fixes/2)
	highlights := []Highlight{{Name: "Before the lock", From: 0, To: 60 * time.Second, Zoom: true}}
	ctx := routeHighlightContext(t, track, fixes, highlights, w, h)
	bm := &fakeBasemap{fill: basemapGreen}
	ctx.Basemap = bm
	rec := &recordProgress{}
	ctx.BasemapProgress = rec.report

	RoutePanel{}.Prepare(ctx, Box{X: 40, Y: 40, W: w - 80, H: h - 80})

	if bm.calls != 1 {
		t.Fatalf("fetched %d views, want 1 (the whole course only); the fixture's highlight resolved a zoom it should not have", bm.calls)
	}
	for i, c := range rec.snapshot() {
		if c[1] != 1 {
			t.Errorf("call %d reports a total of %d, want 1: a highlight that resolved no zoom was counted as a view nobody fetches", i, c[1])
		}
	}
}
