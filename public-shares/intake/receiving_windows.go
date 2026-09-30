package intake

import "io"

// Windows is a portable test target, not the public server. Do not infer
// receiver death from the marker-only Windows budget lock.
type receivingNoop struct{}

func (receivingNoop) Close() error                   { return nil }
func createReceivingLease(string) (io.Closer, error) { return receivingNoop{}, nil }
func (s Store) sweepReceiving() error                { return nil }
