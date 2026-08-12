package sqlstore

import "testing"

func TestRebind(t *testing.T) {
	cases := []struct{ in, want string }{
		{"SELECT 1", "SELECT 1"},
		{"SELECT * FROM t WHERE a = ? AND b = ?", "SELECT * FROM t WHERE a = $1 AND b = $2"},
		{"UPDATE t SET a = ? WHERE id = ? AND s IN (?, ?)", "UPDATE t SET a = $1 WHERE id = $2 AND s IN ($3, $4)"},
		// A question mark inside a literal is data, not a placeholder.
		{"SELECT * FROM t WHERE note = 'why?' AND id = ?", "SELECT * FROM t WHERE note = 'why?' AND id = $1"},
	}
	for _, c := range cases {
		if got := rebind(postgresDialect, c.in); got != c.want {
			t.Errorf("postgres: %q\n got %q\nwant %q", c.in, got, c.want)
		}
		if got := rebind(sqliteDialect, c.in); got != c.in {
			t.Errorf("sqlite must not rewrite: %q -> %q", c.in, got)
		}
	}
}
