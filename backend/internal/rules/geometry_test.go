package rules_test

import (
	"testing"

	"aegisvision/backend/internal/domain"
	"aegisvision/backend/internal/rules"
)

func TestGeometryHitModes(t *testing.T) {
	square := rules.ROI{
		ID:      "roi-square",
		Name:    "Center Box",
		ROIType: domain.ROITypePolygon,
		Points: []rules.Point{
			{X: 0.2, Y: 0.2},
			{X: 0.8, Y: 0.2},
			{X: 0.8, Y: 0.8},
			{X: 0.2, Y: 0.8},
		},
	}

	fullFrame := rules.ROI{
		ID:      "roi-full",
		Name:    "Full Frame",
		ROIType: domain.ROITypeFullFrame,
	}

	// BBox whose bottom_center is (0.5, 0.85) [outside], center is (0.5, 0.70) [inside],
	// and intersects the bottom edge of square.
	bottomOutCenterIn := [4]float64{0.4, 0.55, 0.6, 0.85}

	if rules.DetectionHitsROI(bottomOutCenterIn, square, domain.HitPointBottomCenter) {
		t.Error("expected bottom_center (0.5, 0.85) to be OUTSIDE square [0.2,0.8]")
	}
	if !rules.DetectionHitsROI(bottomOutCenterIn, square, domain.HitPointCenter) {
		t.Error("expected center (0.5, 0.70) to be INSIDE square [0.2,0.8]")
	}
	if !rules.DetectionHitsROI(bottomOutCenterIn, square, domain.HitPointBBoxIntersect) {
		t.Error("expected bbox_intersect to hit square [0.2,0.8]")
	}

	// BBox whose bottom_center (0.5, 0.75) is inside, even though top is outside
	bottomIn := [4]float64{0.4, 0.05, 0.6, 0.75}
	if !rules.DetectionHitsROI(bottomIn, square, domain.HitPointBottomCenter) {
		t.Error("expected bottom_center (0.5, 0.75) to be INSIDE square")
	}

	// Completely outside bbox that still hits full_frame
	farOutside := [4]float64{0.01, 0.01, 0.1, 0.1}
	if rules.DetectionHitsROI(farOutside, square, domain.HitPointBBoxIntersect) {
		t.Error("expected farOutside bbox NOT to intersect square")
	}
	if !rules.DetectionHitsROI(farOutside, fullFrame, domain.HitPointBottomCenter) {
		t.Error("expected any bbox to hit full_frame ROI")
	}

	// Edge-crossing bbox with no corner inside polygon and no polygon vertex inside bbox (cross shape)
	crossStrip := [4]float64{0.1, 0.45, 0.9, 0.55}
	if !rules.DetectionHitsROI(crossStrip, square, domain.HitPointBBoxIntersect) {
		t.Error("expected horizontal strip crossing polygon edges to hit under bbox_intersect")
	}
}
