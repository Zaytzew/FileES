package memoryguard

import "testing"

func TestNativeMemorySample(t *testing.T) {
	s, err := ReadSample()
	if err != nil {
		t.Fatal(err)
	}
	if s.PrivateBytes == 0 || s.TotalBytes == 0 || s.AvailableBytes > s.TotalBytes || s.Goroutines < 1 {
		t.Fatalf("invalid native sample: %+v", s)
	}
}
