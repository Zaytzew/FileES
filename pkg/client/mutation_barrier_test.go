package client

import "testing"

func TestMutationBarrierCoversRemoteWritesOnly(t *testing.T) {
	starts, ends := 0, 0
	c := &execClient{beginMutation: func() func() { starts++; return func() { ends++ } }}
	for _, verb := range []string{"commit", "lock", "unlock", "remote-tree-move", "remote-file-delete"} {
		before := starts
		done := c.mutationBarrier([]string{verb})
		if starts != before+1 {
			t.Fatal(verb)
		}
		done()
	}
	for _, verb := range []string{"info", "status", "list-tree", "update", "checkout", "local-tree", "local-file-delete", "--version"} {
		c.mutationBarrier([]string{verb})()
	}
	if starts != 5 || ends != 5 {
		t.Fatal(starts, ends)
	}
}
