package app

import (
	contract "filees/pkg/contract/v1"
	"testing"
)

func TestServerCapacityProjectionDoesNotChangeRepositoryCreationRights(t *testing.T) {
	s := newAppState()
	s.system.Activations = []contract.ActivationStatus{{ServerID: "spot", ClientRole: contract.ClientRoleReadOnly, StorageCapacity: &contract.StorageCapacityStatus{State: "stale", TotalBytes: 1000, AvailableBytes: 200}}}
	vm := s.viewModel()
	if len(vm.Servers) != 1 || vm.Servers[0].StorageCapacity.AvailableBytes != 200 || vm.Servers[0].CanOfferRepositoryCreation() {
		t.Fatal(vm.Servers)
	}
}
