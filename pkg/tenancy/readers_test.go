package tenancy

import (
	"reflect"
	"testing"
)

var testWorkloads = []Workload{
	{Group: "devel:app:metrics-gate", Cluster: "kernel", Namespace: "gate"},
	{Group: "stage:app:metrics-gate", Cluster: "kernel", Namespace: "gate"},
	{Group: "devel:app:trace-reader", Cluster: "devel", Namespace: "t"},
	{Group: "all:mcp:store-kernel", Cluster: "kernel", Namespace: "mcp"},
	{Group: "all:mcp:store-devel", Cluster: "kernel", Namespace: "mcp"},
	{Group: "bad:group", Cluster: "kernel"},
}

func TestDeriveMachineReaders(t *testing.T) {
	got := DeriveMachineReaders(testWorkloads, []string{"devel"}, "metrics-gate")
	want := []MachineReader{{Group: "devel:app:metrics-gate", Audience: "metrics-gate", Namespace: "app", Scope: "devel"}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}

	if DeriveMachineReaders(testWorkloads, nil, "metrics-gate") != nil {
		t.Fatal("no served scopes must yield nil")
	}
}

func TestDeriveStoreReaders(t *testing.T) {
	clients := []ExchangeClient{{Name: "mcp-client", Requires: []string{"all:mcp:store-kernel"}}}

	got := DeriveStoreReaders(testWorkloads, clients, "kernel", []string{"kernel", "stage"}, "mcp", "store-")
	want := []StoreReader{{Group: "all:mcp:store-kernel", Audience: "mcp-client", Namespace: "mcp", Scopes: []string{"kernel", "stage"}}}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}

	got = DeriveStoreReaders(testWorkloads, clients, "devel", []string{"devel"}, "mcp", "store-")
	if len(got) != 1 || got[0].Namespace != "" || got[0].Audience != "" {
		t.Fatalf("remote workload: %+v", got)
	}
}
