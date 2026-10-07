package tenancy

import (
	"slices"
	"sort"
	"strings"
)

// Workload is one machine identity an estate has declared: a group of the
// form `<cluster>:<namespace>:<role>` plus where its ServiceAccount runs.
// The three-part group is the whole naming convention this file reads; the
// roles and the middle segment it looks for are the caller's inputs.
type Workload struct {
	// Group is the workload's own group name, three non-empty segments
	// separated by colons.
	Group string
	// Cluster is the cluster the workload's ServiceAccount runs in.
	Cluster string
	// Namespace is the namespace the workload's ServiceAccount runs in.
	Namespace string
}

// ExchangeClient is one token-exchange client and the groups a token it
// mints must carry.
type ExchangeClient struct {
	Name     string
	Requires []string
}

// MachineReader is one machine's read-only principal: a workload reading
// exactly one namespace on one cluster.
type MachineReader struct {
	// Group is the workload's group, the principal's group matcher.
	Group string `yaml:"group"`
	// Audience is the group's role segment, pinned as the principal's own
	// audience.
	Audience string `yaml:"audience"`
	// Namespace is the group's middle segment: the one namespace the grant
	// admits.
	Namespace string `yaml:"namespace"`
	// Scope is the group's first segment: the cluster label the grant
	// admits.
	Scope string `yaml:"scope"`
}

// StoreReader is one connector's outbound principal in an install's
// telemetry proxy: it reads every namespace of every served cluster.
type StoreReader struct {
	// Group is the workload's group, the principal's group matcher.
	Group string `yaml:"group"`
	// Audience is the exchange client that requires Group.
	Audience string `yaml:"audience"`
	// Namespace is the workload's own ServiceAccount namespace when it runs
	// in the install's cluster; empty when it runs elsewhere.
	Namespace string `yaml:"namespace,omitempty"`
	// Scopes are every cluster the install serves.
	Scopes []string `yaml:"scopes"`
}

// splitGroup reads `a:b:c` into its parts; false for any other shape.
func splitGroup(name string) (scope, thing, role string, ok bool) {
	parts := strings.Split(name, ":")
	if len(parts) != 3 {
		return "", "", "", false
	}

	for _, part := range parts {
		if part == "" {
			return "", "", "", false
		}
	}

	return parts[0], parts[1], parts[2], true
}

func sortedGroups(workloads []Workload) []Workload {
	out := slices.Clone(workloads)
	sort.Slice(out, func(i, j int) bool { return out[i].Group < out[j].Group })

	return out
}

// DeriveMachineReaders builds one role's principals: one per workload whose
// group has exactly this role and whose own cluster is one the install
// serves. Output is sorted by group.
func DeriveMachineReaders(workloads []Workload, servedScopes []string, role string) []MachineReader {
	if len(servedScopes) == 0 {
		return nil
	}

	var out []MachineReader

	for _, w := range sortedGroups(workloads) {
		scope, thing, workloadRole, ok := splitGroup(w.Group)
		if !ok || workloadRole != role || !slices.Contains(servedScopes, scope) {
			continue
		}

		out = append(out, MachineReader{Group: w.Group, Audience: workloadRole, Namespace: thing, Scope: scope})
	}

	return out
}

// DeriveStoreReaders builds an install's connector principals: one per
// workload whose middle segment is `thing` and whose role is
// rolePrefix+cluster, audience being the exchange client that requires the
// group. Each reads all namespaces of all served clusters. Output is sorted
// by group.
func DeriveStoreReaders(workloads []Workload, clients []ExchangeClient, cluster string, servedScopes []string, thing, rolePrefix string) []StoreReader {
	if len(servedScopes) == 0 {
		return nil
	}

	audiences := map[string]string{}

	for _, client := range clients {
		for _, group := range client.Requires {
			audiences[group] = client.Name
		}
	}

	var out []StoreReader

	for _, w := range sortedGroups(workloads) {
		_, middle, role, ok := splitGroup(w.Group)
		if !ok || middle != thing || role != rolePrefix+cluster {
			continue
		}

		reader := StoreReader{Group: w.Group, Audience: audiences[w.Group], Scopes: slices.Clone(servedScopes)}
		if w.Cluster == cluster {
			reader.Namespace = w.Namespace
		}

		out = append(out, reader)
	}

	return out
}
