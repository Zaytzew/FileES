package main

import (
	"testing"

	"filees/pkg/whaleclient"
)

func TestWhaleProjectionCarriesDurableSpoolBinding(t *testing.T) {
	operation := whaleclient.Operation{
		OperationID: "operation", SpoolRoot: `E:\.filees-whales`,
		SpoolVolumeID: "volume-e", SpoolDeviceID: "disk:0", ReservedBytes: 17_303_798_784,
	}
	projection := projectWhaleOperation(operation)
	if projection.SpoolRoot != operation.SpoolRoot || projection.SpoolVolumeID != operation.SpoolVolumeID || projection.SpoolDeviceID != operation.SpoolDeviceID || projection.ReservedBytes != operation.ReservedBytes {
		t.Fatalf("projection=%+v", projection)
	}
}
