package modules

import "testing"

func TestBuiltinCheckin(t *testing.T) {
	list := Builtin()
	if len(list) == 0 {
		t.Fatal("expected at least one module")
	}
	found := false
	for _, m := range list {
		if m.ID == "checkin" {
			found = true
			if m.UserPath == "" || m.AdminPath == "" {
				t.Fatalf("checkin paths missing: %+v", m)
			}
			if m.Status != "active" {
				t.Fatalf("checkin should be active, got %s", m.Status)
			}
		}
	}
	if !found {
		t.Fatal("checkin module not registered")
	}
	ids := ActiveIDs()
	if len(ids) == 0 || ids[0] != "checkin" {
		t.Fatalf("ActiveIDs=%v", ids)
	}
	if ProductID != "sub2api-ext" || ProjectName != "sub2api-ext" || CompatName != ProjectName {
		t.Fatalf("product identity mismatch: %s / %s / %s", ProductID, ProjectName, CompatName)
	}
}
