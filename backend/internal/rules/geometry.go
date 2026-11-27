package rules

import (
	"math"

	"aegisvision/backend/internal/domain"
)

// Point is a normalized [0.0, 1.0] 2D coordinate.
type Point struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
}

// ROI defines a detection region on a camera frame.
type ROI struct {
	ID      string  `json:"id"`
	Name    string  `json:"name"`
	ROIType string  `json:"roi_type"`
	Points  []Point `json:"points"`
}

// DetectionHitsROI evaluates whether bbox [x1, y1, x2, y2] hits roi under hitPoint mode.
func DetectionHitsROI(bbox [4]float64, roi ROI, hitPoint string) bool {
	if roi.ROIType == domain.ROITypeFullFrame {
		return true
	}
	if len(roi.Points) < 3 {
		return false
	}

	x1, y1, x2, y2 := bbox[0], bbox[1], bbox[2], bbox[3]
	if x2 < x1 {
		x1, x2 = x2, x1
	}
	if y2 < y1 {
		y1, y2 = y2, y1
	}

	switch hitPoint {
	case domain.HitPointCenter:
		return PointInPolygon(Point{X: (x1 + x2) / 2.0, Y: (y1 + y2) / 2.0}, roi.Points)
	case domain.HitPointBBoxIntersect:
		return bboxIntersectsPolygon(x1, y1, x2, y2, roi.Points)
	case domain.HitPointBottomCenter, "":
		return PointInPolygon(Point{X: (x1 + x2) / 2.0, Y: y2}, roi.Points)
	default:
		return PointInPolygon(Point{X: (x1 + x2) / 2.0, Y: y2}, roi.Points)
	}
}

// PointInPolygon determines whether pt lies inside or on the boundary of poly using ray casting.
func PointInPolygon(pt Point, poly []Point) bool {
	n := len(poly)
	if n < 3 {
		return false
	}
	inside := false
	j := n - 1
	for i := 0; i < n; i++ {
		pi, pj := poly[i], poly[j]
		if pointOnSegment(pt, pj, pi) {
			return true
		}
		if (pi.Y > pt.Y) != (pj.Y > pt.Y) {
			xIntersect := (pj.X-pi.X)*(pt.Y-pi.Y)/(pj.Y-pi.Y) + pi.X
			if pt.X <= xIntersect {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}

func bboxIntersectsPolygon(x1, y1, x2, y2 float64, poly []Point) bool {
	corners := [4]Point{
		{X: x1, Y: y1},
		{X: x2, Y: y1},
		{X: x2, Y: y2},
		{X: x1, Y: y2},
	}

	// 1. Any bbox corner inside polygon
	for _, c := range corners {
		if PointInPolygon(c, poly) {
			return true
		}
	}

	// 2. Any polygon vertex inside bbox
	for _, p := range poly {
		if p.X >= x1 && p.X <= x2 && p.Y >= y1 && p.Y <= y2 {
			return true
		}
	}

	// 3. Any bbox edge intersects any polygon edge
	n := len(poly)
	for i := 0; i < 4; i++ {
		a1, a2 := corners[i], corners[(i+1)%4]
		for j := 0; j < n; j++ {
			b1, b2 := poly[j], poly[(j+1)%n]
			if segmentsIntersect(a1, a2, b1, b2) {
				return true
			}
		}
	}
	return false
}

const geomEps = 1e-9

func cross(o, a, b Point) float64 {
	return (a.X-o.X)*(b.Y-o.Y) - (a.Y-o.Y)*(b.X-o.X)
}

func pointOnSegment(p, a, b Point) bool {
	if math.Abs(cross(a, b, p)) > geomEps {
		return false
	}
	return p.X >= math.Min(a.X, b.X)-geomEps &&
		p.X <= math.Max(a.X, b.X)+geomEps &&
		p.Y >= math.Min(a.Y, b.Y)-geomEps &&
		p.Y <= math.Max(a.Y, b.Y)+geomEps
}

func segmentsIntersect(a1, a2, b1, b2 Point) bool {
	d1 := cross(b1, b2, a1)
	d2 := cross(b1, b2, a2)
	d3 := cross(a1, a2, b1)
	d4 := cross(a1, a2, b2)

	if ((d1 > geomEps && d2 < -geomEps) || (d1 < -geomEps && d2 > geomEps)) &&
		((d3 > geomEps && d4 < -geomEps) || (d3 < -geomEps && d4 > geomEps)) {
		return true
	}
	if math.Abs(d1) <= geomEps && pointOnSegment(a1, b1, b2) {
		return true
	}
	if math.Abs(d2) <= geomEps && pointOnSegment(a2, b1, b2) {
		return true
	}
	if math.Abs(d3) <= geomEps && pointOnSegment(b1, a1, a2) {
		return true
	}
	if math.Abs(d4) <= geomEps && pointOnSegment(b2, a1, a2) {
		return true
	}
	return false
}
