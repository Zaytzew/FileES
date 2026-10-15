package main

import (
	"reflect"
	"testing"
)

func TestLifecycleRecordsBeforeCancellationOnce(t *testing.T) {
	for _, restart := range []bool{false, true} {
		var got []string
		l := &daemonLifecycle{cancel: func() { got = append(got, "cancel") }, record: func(s string) { got = append(got, s) }}
		phase := "stopping"
		if restart {
			phase = "restarting"
			l.Restart()
		} else {
			l.Shutdown()
		}
		l.Shutdown()
		l.Restart()
		if !reflect.DeepEqual(got, []string{phase, "cancel"}) {
			t.Fatal(got)
		}
	}
}
