package worktree

import "testing"

func TestParseList_MainFirstAndAttributes(t *testing.T) {
	porcelain := `worktree /repo/main
HEAD abc123
branch refs/heads/main

worktree /repo/wt-detached
HEAD def456
detached

worktree /repo/wt-locked
HEAD 111222
branch refs/heads/feat-locked
locked usb drive

worktree /repo/wt-prune
HEAD 333444
branch refs/heads/feat-prune
prunable gitdir file points to non-existent location
`
	got := parseList(porcelain)
	if len(got) != 4 {
		t.Fatalf("expected 4 worktrees, got %d", len(got))
	}
	if !got[0].IsMain {
		t.Errorf("first stanza must be main")
	}
	for _, wt := range got[1:] {
		if wt.IsMain {
			t.Errorf("only the first stanza is main: %+v", wt)
		}
	}
	if got[0].Branch != "main" {
		t.Errorf("refs/heads/ prefix must be stripped, got %q", got[0].Branch)
	}
	if !got[1].Detached {
		t.Errorf("expected detached")
	}
	if !got[2].Locked || got[2].LockReason != "usb drive" {
		t.Errorf("expected locked with reason, got %+v", got[2])
	}
	if !got[3].Prunable {
		t.Errorf("expected prunable, got %+v", got[3])
	}
}

func TestParseAhead(t *testing.T) {
	cases := map[string]int{
		"+0 -0":  0,
		"+3 -0":  3,
		"+12 -5": 12,
		"":       0,
	}
	for in, want := range cases {
		if got := parseAhead(in); got != want {
			t.Errorf("parseAhead(%q) = %d, want %d", in, got, want)
		}
	}
}
