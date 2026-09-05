package ipcserver

import (
	"encoding/json"
	"sync"
	"testing"

	contract "filees/pkg/contract/v1"
)

func TestBrokerNameWinsOverConcurrentOldViewsWithoutChangingIdentity(t *testing.T) {
	s := New("unused")
	events := make(chan contract.Event, 256)
	s.addSub(events)
	original := contract.ActivationStatus{ServerID: "spot", DisplayName: "spot", Address: "spot.example.net:2223", ClientID: "client", RealmID: "realm", SSHPort: 2223}
	s.RegisterActivation(original)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.RegisterActivation(original)
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			s.SetBrokerServerDisplayName("spot", "40rs:filees")
		}
	}()
	wg.Wait()
	close(events)
	s.removeSub(events)
	sawBroker := false
	for event := range events {
		var status contract.ActivationStatus
		if err := json.Unmarshal(event.Payload, &status); err != nil {
			t.Fatal(err)
		}
		if status.DisplayName == "40rs:filees" {
			sawBroker = true
		} else if sawBroker {
			t.Fatal("old label emitted after broker name")
		}
	}
	if !sawBroker {
		t.Fatal("broker name event missing")
	}
	original.DisplayName = "40rs:filees"
	if got := s.allActivations(); len(got) != 1 || got[0] != original {
		t.Fatalf("name/identity=%+v", got)
	}
	s.SetBrokerServerDisplayName("spot", "Nazwa zmieniona")
	if got := s.allActivations()[0]; got.DisplayName != "Nazwa zmieniona" {
		t.Fatalf("name=%s", got.DisplayName)
	}
	s.RemoveServer("spot")
	s.RegisterActivation(contract.ActivationStatus{ServerID: "spot", DisplayName: "new-bootstrap"})
	if got := s.allActivations()[0]; got.DisplayName != "new-bootstrap" {
		t.Fatal("removed profile retained overlay")
	}
}
