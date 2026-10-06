package store

import (
	"context"
	"database/sql"
	"testing"
)

func TestUsersCRUDAndValidation(t *testing.T) {
	st, ctx := testStore(t), context.Background()
	if err := st.EnsureUser(ctx, "admin", "hash-admin", "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.EnsureUser(ctx, "admin", "hash-new", "viewer"); err != nil {
		t.Fatal(err)
	}
	user, err := st.GetUser(ctx, "admin")
	if err != nil || user.PasswordHash != "hash-admin" || user.Role != "admin" || !user.Enabled {
		t.Fatalf("unexpected seeded user: %+v err=%v", user, err)
	}
	if err := st.CreateUser(ctx, User{Username: "viewer", PasswordHash: "hash-viewer", Role: "viewer", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateUser(ctx, "viewer", "operator", false); err != nil {
		t.Fatal(err)
	}
	user, err = st.GetUser(ctx, "viewer")
	if err != nil || user.Role != "operator" || user.Enabled {
		t.Fatalf("unexpected updated user: %+v err=%v", user, err)
	}
	if err := st.DeleteUser(ctx, "viewer"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetUser(ctx, "viewer"); err != sql.ErrNoRows {
		t.Fatalf("deleted user still exists: %v", err)
	}
}
