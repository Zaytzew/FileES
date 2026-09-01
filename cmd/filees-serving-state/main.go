// Command filees-serving-state is the FileES server's reservation
// (lock) projection worker. It is deliberately its own binary — not a
// subcommand of filees-worker or filees-client-entry — per
// concepts/fixture_plan.md r680 and
// concepts/RESERVATION_SERVER_EMISSION_WORKPLAN.md: the existing
// transactional workers stay unaware of this protocol, and this binary can
// be built, packaged and updated independently of them.
package main

import (
	"os"

	"filees/internal/servertool"
)

func main() {
	os.Exit(servertool.RunReservationProjectionWorker(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
