package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/margo/sandbox/standard/generatedCode/wfm/sbi"
	yamlv3 "gopkg.in/yaml.v3"
)

type CpuAssignment struct {
	Requirement string
	Cpus        []int
}

func (dm *DeploymentManager) resolveComponentCpuAssignments(deploymentID string, componentName string,
	requiredResources *sbi.RequiredResources, existingAssignments map[string][]int) ([]CpuAssignment, error) {
	if requiredResources == nil || requiredResources.Cpu == nil || len(*requiredResources.Cpu) == 0 {
		return nil, nil
	}

	if len(dm.topologyLookup.IsolatedCPUIndices) == 0 {
		return nil, nil
	}

	owner := NewOwnerRef(deploymentID, componentName)

	// Membership is structural - requiredResources is nested inside the component - so
	// requiredResources[].name is neither read nor matched against the component name.
	reqs := make([]sbi.Cpu, 0, len(*requiredResources.Cpu))
	for _, req := range *requiredResources.Cpu {
		if req.Type != nil && *req.Type == sbi.CpuTypeIsolated {
			reqs = append(reqs, req)
		}
	}

	if len(reqs) == 0 {
		return nil, nil
	}

	// One component deploys one unit with one cpuset, so a second isolated requirement
	// has nowhere to go and would otherwise alias onto the first one's CPUs.
	if len(reqs) > 1 {
		return nil, fmt.Errorf("component %q declares %d isolated CPU requirements; only one is supported", componentName, len(reqs))
	}

	taken := allocatedCPUOwners(dm.database.AllocatedCpus())
	for requirement, cpuIndices := range existingAssignments {
		holder := NewOwnerRef(deploymentID, requirement)

		for _, cpuIndex := range cpuIndices {
			if _, exists := dm.topologyLookup.IsolatedCPUSet[cpuIndex]; !exists {
				continue
			}
			taken[cpuIndex] = holder
		}
	}

	assignments := make([]CpuAssignment, 0, len(reqs))

	for _, req := range reqs {
		requiredCores := int64(1)
		if req.Cores != nil && *req.Cores > 0 {
			requiredCores = int64(math.Ceil(float64(*req.Cores)))
		}

		candidates := dm.topologyLookup.IsolatedCPUIndices
		if len(candidates) == 0 {
			return nil, fmt.Errorf("no isolated CPUs available for requirement %q", owner.Ref)
		}

		selected := make([]int, 0, requiredCores)
		for _, cpu := range candidates {
			if !owner.CanTake(taken[cpu]) {
				continue
			}
			selected = append(selected, cpu)
			if len(selected) == int(requiredCores) {
				break
			}
		}

		if len(selected) < int(requiredCores) {
			return nil, fmt.Errorf("no free isolated CPUs available for requirement %q (required=%d)", owner.Ref, requiredCores)
		}

		for _, cpu := range selected {
			taken[cpu] = owner
		}

		assignments = append(assignments, CpuAssignment{
			Requirement: string(owner.Ref),
			Cpus:        selected,
		})
	}

	return assignments, nil
}

func rewriteComposeFile(sourcePath string, targetPath string, assignments []CpuAssignment) error {
	in, err := os.Open(filepath.Clean(sourcePath))
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(filepath.Clean(targetPath))
	if err != nil {
		return err
	}

	rewriteErr := RewriteComposeYAML(in, out, assignments)
	closeErr := out.Close()
	if rewriteErr != nil {
		return rewriteErr
	}
	if closeErr != nil {
		return closeErr
	}

	return nil
}

func RewriteComposeYAML(in io.Reader, out io.Writer, assignments []CpuAssignment) error {
	if len(assignments) == 0 {
		_, err := io.Copy(out, in)
		return err
	}

	data, err := io.ReadAll(in)
	if err != nil {
		return fmt.Errorf("read compose file: %w", err)
	}

	var root yamlv3.Node
	if err := yamlv3.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("parse compose yaml: %w", err)
	}

	serviceMap, err := composeServicesNode(&root)
	if err != nil {
		return err
	}

	// TODO: this should be renamed as assignmentsByRequirementName
	// the name is currently misleading
	assignmentByService := toAssignmentMap(assignments)
	for serviceName, cpus := range assignmentByService {
		serviceNode, exists := serviceMap[serviceName]
		if !exists {
			return fmt.Errorf("assignment references unknown compose service %q", serviceName)
		}

		cpuset := formatCPUSet(cpus)
		if err := setServiceCPuset(serviceNode, cpuset); err != nil {
			return fmt.Errorf("set cpuset for service %q: %w", serviceName, err)
		}

		if err := setServiceEnvironmentVariable(serviceNode, "TEST_CPUSET", cpuset); err != nil {
			return fmt.Errorf("set TEST_CPUSET environment variable for service %q: %w", serviceName, err)
		}
	}

	encoder := yamlv3.NewEncoder(out)
	encoder.SetIndent(2)
	if err := encoder.Encode(&root); err != nil {
		return fmt.Errorf("encode patched compose yaml: %w", err)
	}

	return nil
}

func composeServicesNode(root *yamlv3.Node) (map[string]*yamlv3.Node, error) {
	if root == nil || len(root.Content) == 0 {
		return nil, fmt.Errorf("compose yaml is empty")
	}
	doc := root.Content[0]
	if doc.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("compose yaml root must be a mapping")
	}

	services := mappingValueByKey(doc, "services")
	if services == nil {
		return map[string]*yamlv3.Node{}, nil
	}
	if services.Kind != yamlv3.MappingNode {
		return nil, fmt.Errorf("compose services must be a mapping")
	}

	result := make(map[string]*yamlv3.Node)
	for i := 0; i+1 < len(services.Content); i += 2 {
		nameNode := services.Content[i]
		valueNode := services.Content[i+1]
		// TODO: hardcoding the "_compose" suffix here
		// to deal with multiple services inside the docker-compose.yaml
		// file for the same component. Once we decide there
		// will be only one component per compose file, we can remove this suffix.
		// or we add another field to map component to service name
		result[nameNode.Value+"_compose"] = valueNode
	}
	return result, nil
}

func mappingValueByKey(mapping *yamlv3.Node, key string) *yamlv3.Node {
	if mapping == nil || mapping.Kind != yamlv3.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setServiceCPuset(serviceNode *yamlv3.Node, cpuset string) error {
	if serviceNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service definition must be a mapping")
	}

	for i := 0; i+1 < len(serviceNode.Content); i += 2 {
		if serviceNode.Content[i].Value == "cpuset" {
			serviceNode.Content[i+1].Kind = yamlv3.ScalarNode
			serviceNode.Content[i+1].Tag = "!!str"
			serviceNode.Content[i+1].Value = cpuset
			return nil
		}
	}

	serviceNode.Content = append(serviceNode.Content,
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: "cpuset"},
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: cpuset},
	)
	return nil
}

func setServiceEnvironmentVariable(serviceNode *yamlv3.Node, varName string, varValue string) error {
	if serviceNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service definition must be a mapping")
	}

	// Find or create the environment section
	var envNode *yamlv3.Node
	for i := 0; i+1 < len(serviceNode.Content); i += 2 {
		if serviceNode.Content[i].Value == "environment" {
			envNode = serviceNode.Content[i+1]
			break
		}
	}

	// If environment doesn't exist, create it
	if envNode == nil {
		envNode = &yamlv3.Node{
			Kind:    yamlv3.MappingNode,
			Tag:     "!!map",
			Content: []*yamlv3.Node{},
		}
		serviceNode.Content = append(serviceNode.Content,
			&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: "environment"},
			envNode,
		)
	}

	if envNode.Kind != yamlv3.MappingNode {
		return fmt.Errorf("service environment must be a mapping")
	}

	// Look for existing variable
	for i := 0; i+1 < len(envNode.Content); i += 2 {
		if envNode.Content[i].Value == varName {
			envNode.Content[i+1].Kind = yamlv3.ScalarNode
			envNode.Content[i+1].Tag = "!!str"
			envNode.Content[i+1].Value = varValue
			return nil
		}
	}

	// If not found, add it
	envNode.Content = append(envNode.Content,
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: varName},
		&yamlv3.Node{Kind: yamlv3.ScalarNode, Tag: "!!str", Value: varValue},
	)
	return nil
}

func toAssignmentMap(assignments []CpuAssignment) map[string][]int {
	result := make(map[string][]int, len(assignments))
	for _, assignment := range assignments {
		copied := make([]int, len(assignment.Cpus))
		copy(copied, assignment.Cpus)
		result[assignment.Requirement] = copied
	}
	return result
}

func formatCPUSet(cpus []int) string {
	if len(cpus) == 0 {
		return ""
	}

	sorted := make([]int, len(cpus))
	copy(sorted, cpus)
	sort.Ints(sorted)

	compact := make([]string, 0, len(sorted))
	start := sorted[0]
	prev := sorted[0]

	flush := func(s int, e int) {
		if s == e {
			compact = append(compact, strconv.Itoa(s))
			return
		}
		compact = append(compact, fmt.Sprintf("%d-%d", s, e))
	}

	for i := 1; i < len(sorted); i++ {
		current := sorted[i]
		if current == prev+1 {
			prev = current
			continue
		}
		flush(start, prev)
		start = current
		prev = current
	}
	flush(start, prev)

	return strings.Join(compact, ",")
}
