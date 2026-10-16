package client

// Remote writes only: local status, hydration and WC housekeeping do not
// invalidate server facts. Both native and legacy entry points share this list.
func (c *execClient) mutationBarrier(args []string) func() {
	if len(args) > 0 && c.beginMutation != nil {
		switch args[0] {
		case "commit", "lock", "unlock", "remote-tree-move", "remote-file-delete":
			if finish := c.beginMutation(); finish != nil {
				return finish
			}
		}
	}
	return func() {}
}
