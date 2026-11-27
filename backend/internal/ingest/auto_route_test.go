package ingest

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"aegisvision/backend/internal/platform/postgres/sqlcgen"
)

func TestMatchesChannelAutoRoute_ExplicitGuardAndZoneAncestors(t *testing.T) {
	factoryID := uuid.Must(uuid.NewV7())
	workshopID := uuid.Must(uuid.NewV7())
	stationID := uuid.Must(uuid.NewV7())
	otherWorkshopID := uuid.Must(uuid.NewV7())

	zones := []sqlcgen.Zone{
		{ID: factoryID, Name: "Factory A"},
		{ID: workshopID, ParentID: uuid.NullUUID{UUID: factoryID, Valid: true}, Name: "Workshop 1"},
		{ID: stationID, ParentID: uuid.NullUUID{UUID: workshopID, Valid: true}, Name: "Station 1A"},
		{ID: otherWorkshopID, ParentID: uuid.NullUUID{UUID: factoryID, Valid: true}, Name: "Workshop 2"},
	}

	ancestors := buildZoneAncestorSet(zones, stationID)
	stationNull := uuid.NullUUID{UUID: stationID, Valid: true}

	// Empty routing rules or auto_route=false MUST NOT auto-route.
	if matchesChannelAutoRoute(nil, "critical", "event_detect", stationNull, ancestors) {
		t.Fatal("expected nil routing_rules not to auto-route")
	}
	if matchesChannelAutoRoute(json.RawMessage(`{}`), "critical", "event_detect", stationNull, ancestors) {
		t.Fatal("expected empty routing_rules not to auto-route")
	}
	if matchesChannelAutoRoute(json.RawMessage(`{"auto_route":false,"severities":["critical"]}`), "critical", "event_detect", stationNull, ancestors) {
		t.Fatal("expected auto_route=false not to auto-route")
	}

	// Explicit auto_route=true with parent workshop zone_id MUST match descendant workstation.
	workshopRule, _ := json.Marshal(map[string]any{
		"auto_route": true,
		"severities": []string{"critical", "warning"},
		"zone_ids":   []string{workshopID.String()},
		"rule_modes": []string{"event_detect"},
	})
	if !matchesChannelAutoRoute(workshopRule, "critical", "event_detect", stationNull, ancestors) {
		t.Fatal("expected parent workshop auto_route to match descendant station")
	}
	if matchesChannelAutoRoute(workshopRule, "info", "event_detect", stationNull, ancestors) {
		t.Fatal("expected non-matching severity not to auto-route")
	}
	if matchesChannelAutoRoute(workshopRule, "critical", "sop_sequence", stationNull, ancestors) {
		t.Fatal("expected non-matching rule_mode not to auto-route")
	}

	// Sibling workshop MUST NOT match Station 1A.
	otherWorkshopRule, _ := json.Marshal(map[string]any{
		"auto_route": true,
		"zone_ids":   []string{otherWorkshopID.String()},
	})
	if matchesChannelAutoRoute(otherWorkshopRule, "critical", "event_detect", stationNull, ancestors) {
		t.Fatal("expected unrelated sibling workshop not to match")
	}

	// Camera with no zone MUST NOT match a zone-restricted auto_route.
	if matchesChannelAutoRoute(workshopRule, "critical", "event_detect", uuid.NullUUID{}, nil) {
		t.Fatal("expected camera without zone not to match zone-restricted auto_route")
	}
}
