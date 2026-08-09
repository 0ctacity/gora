package scrollinput

import "testing"

func TestNormalizePhysicalPixelsAcrossMetricsKeepsIndependentSigns(t *testing.T) {
	for _, test := range []struct {
		scale float64
		dx    float64
		dy    float64
		wantX float64
		wantY float64
	}{
		{scale: 1, dx: 18.5, dy: -42, wantX: 19, wantY: -42},
		{scale: 2, dx: 18.5, dy: -42, wantX: 9, wantY: -21},
		{scale: 3, dx: -0.25, dy: 0.25, wantX: -1, wantY: 1},
	} {
		outcome, err := Normalize(Event{Source: "trackpad", DeltaX: test.dx, DeltaY: test.dy, Units: "physical_pixels", Phase: "update", Momentum: "none"}, test.scale)
		if err != nil {
			t.Fatal(err)
		}
		if outcome.LogicalDeltaX != test.wantX || outcome.LogicalDeltaY != test.wantY {
			t.Fatalf("scale=%v outcome=%+v", test.scale, outcome)
		}
	}
}

func TestNormalizeDefaultsOmittedMomentumToNone(t *testing.T) {
	outcome, err := Normalize(Event{Source: "wheel", Units: "logical", Phase: "update"}, 1)
	if err != nil || outcome.Momentum != "none" {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}
