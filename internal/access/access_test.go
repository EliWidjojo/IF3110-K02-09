package access

import "testing"

func TestPolicy(t *testing.T) {
	for _, tc := range []struct {
		name   string
		p      Principal
		db     string
		action Action
		want   bool
	}{
		{"anonymous", Principal{}, "one", ViewData, false},
		{"admin has no implicit database grant", Principal{ID: 1, Admin: true}, "one", WriteMigration, false},
		{"admin manages platform", Principal{ID: 1, Admin: true}, "", ManagePlatform, true},
		{"ordinary user", Principal{ID: 1}, "", ManagePlatform, false},
		{"view history", Principal{ID: 1, Databases: map[string]Level{"one": View}}, "one", ViewHistory, true},
		{"view cannot edit", Principal{ID: 1, Databases: map[string]Level{"one": View}}, "one", WriteMigration, false},
		{"edit cannot cross database", Principal{ID: 1, Databases: map[string]Level{"one": Edit}}, "two", WriteMigration, false},
		{"edit allowed", Principal{ID: 1, Databases: map[string]Level{"one": Edit}}, "one", WriteMigration, true},
		{"unknown action fails closed", Principal{ID: 1, Admin: true, Databases: map[string]Level{"one": Edit}}, "one", "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Allowed(tc.p, tc.db, tc.action); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
	if Merge(View, Edit) != Edit || Merge(Edit, View) != Edit || Merge(None, View) != View {
		t.Fatal("highest grant must win")
	}
}
