package navigation

import (
	"image"
	"strconv"
	"testing"
)

func TestHistoryNavigatesBackForwardAndRestoresEntryScroll(t *testing.T) {
	history := New("home")
	if history.Current() != "home" || history.CanBack() || history.CanForward() {
		t.Fatalf("initial history = %+v", history)
	}

	transition := history.Navigate("reports", map[string]image.Point{"feed": image.Pt(0, 140)})
	if !transition.Changed || transition.Screen != "reports" || len(transition.Scroll) != 0 {
		t.Fatalf("navigate = %+v", transition)
	}
	transition = history.Back(map[string]image.Point{"report-list": image.Pt(0, 80)})
	if !transition.Changed || transition.Screen != "home" || transition.Scroll["feed"].Y != 140 {
		t.Fatalf("back = %+v", transition)
	}
	transition = history.Forward(map[string]image.Point{"feed": image.Pt(0, 160)})
	if !transition.Changed || transition.Screen != "reports" || transition.Scroll["report-list"].Y != 80 {
		t.Fatalf("forward = %+v", transition)
	}
}

func TestHistorySameScreenBoundariesReplaceAndForwardTruncation(t *testing.T) {
	history := New("home")
	if got := history.Back(nil); got.Changed {
		t.Fatalf("back boundary = %+v", got)
	}
	if got := history.Navigate("home", map[string]image.Point{"feed": image.Pt(0, 20)}); got.Changed || history.Len() != 1 {
		t.Fatalf("same-screen navigate = %+v history=%+v", got, history)
	}
	history.Navigate("reports", nil)
	history.Navigate("customers", nil)
	history.Back(nil)
	history.Navigate("revenue", nil)
	if history.CanForward() || history.Current() != "revenue" || history.Len() != 3 {
		t.Fatalf("truncated history = %+v", history)
	}
	if got := history.Replace("overview", map[string]image.Point{"chart": image.Pt(0, 50)}); !got.Changed || got.Screen != "overview" || len(got.Scroll) != 0 || history.Len() != 3 {
		t.Fatalf("replace = %+v history=%+v", got, history)
	}
	if got := history.Replace("overview", nil); got.Changed {
		t.Fatalf("same-screen replace = %+v", got)
	}
}

func TestHistoryIsBoundedToOneHundredEntries(t *testing.T) {
	history := New("screen-0")
	for index := 1; index <= 140; index++ {
		history.Navigate(screenName(index), nil)
	}
	if history.Len() != 100 || history.Current() != "screen-140" {
		t.Fatalf("bounded history = len %d current %q", history.Len(), history.Current())
	}
	for history.CanBack() {
		history.Back(nil)
	}
	if history.Current() != "screen-41" {
		t.Fatalf("oldest retained screen = %q", history.Current())
	}
}

func TestHistoryReconcilesRemovedScreensAndScrollNodes(t *testing.T) {
	history := New("home")
	history.Navigate("reports", map[string]image.Point{"home-feed": image.Pt(0, 40), "removed": image.Pt(0, 90)})
	history.Navigate("customers", map[string]image.Point{"reports-feed": image.Pt(0, 70)})

	transition := history.Reconcile(map[string]map[string]bool{
		"home":      {"home-feed": true},
		"customers": {"customer-feed": true},
	}, "home", map[string]image.Point{"customer-feed": image.Pt(0, 15)})
	if transition.Screen != "customers" || history.Len() != 2 {
		t.Fatalf("reconcile = %+v history=%+v", transition, history)
	}
	transition = history.Back(nil)
	if transition.Screen != "home" || len(transition.Scroll) != 1 || transition.Scroll["home-feed"].Y != 40 {
		t.Fatalf("pruned back entry = %+v", transition)
	}

	transition = history.Reconcile(map[string]map[string]bool{"overview": {}}, "overview", nil)
	if transition.Screen != "overview" || history.Len() != 1 || history.CanBack() || history.CanForward() {
		t.Fatalf("fallback reconcile = %+v history=%+v", transition, history)
	}
}

func screenName(index int) string {
	return "screen-" + strconv.Itoa(index)
}
