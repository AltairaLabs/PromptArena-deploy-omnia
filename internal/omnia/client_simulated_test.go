package omnia

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// simulatedClient is a fake omniaClient for testing that stores resources
// in memory and supports error injection.
type simulatedClient struct {
	mu                sync.Mutex
	resources         map[string]*ResourceResponse
	failOn            map[string]error
	validProviders    map[string]bool
	validSkillSources map[string]bool
	healthy           bool

	// providerSummaries, when set, is returned by ListProviders verbatim;
	// otherwise ListProviders derives a list from validProviders. listProvidersErr
	// forces ListProviders to fail (to exercise the per-ref fallback).
	providerSummaries []ProviderSummary
	listProvidersErr  error

	// toolRegistries is returned by ListToolRegistries verbatim;
	// listToolRegistriesErr forces it to fail (to exercise skip-on-list-error).
	toolRegistries        []ToolRegistrySummary
	listToolRegistriesErr error

	// updateConflictsRemaining makes the next N UpdateResource calls return a
	// 409 Conflict before succeeding (to exercise updateWithRetry).
	updateConflictsRemaining int

	// createAlreadyExists, when keyed by simKey(resType, name), makes the next
	// CreateResource call for that key return a 409 AlreadyExists HTTPError and
	// seed the resource (so the subsequent update succeeds) — to exercise the
	// create→AlreadyExists→update fallback in applyResourcePhase.
	createAlreadyExists map[string]bool
}

// newSimulatedClient creates a simulatedClient with default healthy state.
func newSimulatedClient() *simulatedClient {
	return &simulatedClient{
		resources:         make(map[string]*ResourceResponse),
		failOn:            make(map[string]error),
		validProviders:    make(map[string]bool),
		validSkillSources: make(map[string]bool),
		healthy:           true,
	}
}

func simKey(resType, name string) string {
	return resType + "/" + name
}

func (s *simulatedClient) injectedError(resType, name string) error {
	if err, ok := s.failOn[simKey(resType, name)]; ok {
		return err
	}
	return nil
}

func (s *simulatedClient) CreateResource(
	ctx context.Context, resType, name string, body json.RawMessage,
) (*ResourceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.injectedError(resType, name); err != nil {
		return nil, err
	}

	key := simKey(resType, name)
	if s.createAlreadyExists[key] {
		// Simulate a resource that already exists in the cluster: seed it (so a
		// follow-up update succeeds) and return a 409 AlreadyExists, exactly once.
		delete(s.createAlreadyExists, key)
		if _, exists := s.resources[key]; !exists {
			s.resources[key] = &ResourceResponse{
				Kind:     resType,
				Metadata: ResourceMetadata{Name: name, UID: "uid-" + name, ResourceVersion: "1"},
			}
		}
		return nil, &HTTPError{
			StatusCode: httpStatusConflict,
			Body:       `{"reason":"AlreadyExists","message":"object already exists"}`,
			Category:   ErrCategoryConflict,
		}
	}
	if _, exists := s.resources[key]; exists {
		return nil, fmt.Errorf("resource %s already exists", key)
	}

	resp := &ResourceResponse{
		Kind: resType,
		Metadata: ResourceMetadata{
			Name:            name,
			UID:             "uid-" + name,
			ResourceVersion: "1",
		},
		Spec: body,
	}
	s.resources[key] = resp
	return resp, nil
}

func (s *simulatedClient) GetResource(
	ctx context.Context, resType, name string,
) (*ResourceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.injectedError(resType, name); err != nil {
		return nil, err
	}

	key := simKey(resType, name)
	res, ok := s.resources[key]
	if !ok {
		return nil, fmt.Errorf("resource %s not found", key)
	}
	return res, nil
}

func (s *simulatedClient) UpdateResource(
	ctx context.Context, resType, name string, body json.RawMessage,
) (*ResourceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.updateConflictsRemaining > 0 {
		s.updateConflictsRemaining--
		return nil, &HTTPError{
			StatusCode: httpStatusConflict,
			Body:       `{"reason":"Conflict","message":"the object has been modified"}`,
			Category:   ErrCategoryConflict,
		}
	}

	if err := s.injectedError(resType, name); err != nil {
		return nil, err
	}

	key := simKey(resType, name)
	existing, ok := s.resources[key]
	if !ok {
		return nil, fmt.Errorf("resource %s not found", key)
	}

	// Increment resource version.
	ver, _ := strconv.Atoi(existing.Metadata.ResourceVersion)
	existing.Metadata.ResourceVersion = strconv.Itoa(ver + 1)
	existing.Spec = body
	return existing, nil
}

func (s *simulatedClient) DeleteResource(
	ctx context.Context, resType, name string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.injectedError(resType, name); err != nil {
		return err
	}

	key := simKey(resType, name)
	if _, ok := s.resources[key]; !ok {
		return fmt.Errorf("resource %s not found", key)
	}
	delete(s.resources, key)
	return nil
}

func (s *simulatedClient) ListResources(
	ctx context.Context, resType, labelSelector string,
) ([]ResourceResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.injectedError(resType, ""); err != nil {
		return nil, err
	}

	var results []ResourceResponse
	prefix := resType + "/"
	for key, res := range s.resources {
		if strings.HasPrefix(key, prefix) {
			if labelSelector == "" || matchesSelector(res.Metadata.Labels, labelSelector) {
				results = append(results, *res)
			}
		}
	}
	return results, nil
}

func (s *simulatedClient) ValidateProvider(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.validProviders[name] {
		return nil
	}
	return fmt.Errorf("provider %q not found", name)
}

func (s *simulatedClient) ListProviders(_ context.Context) ([]ProviderSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listProvidersErr != nil {
		return nil, s.listProvidersErr
	}
	if s.providerSummaries != nil {
		return s.providerSummaries, nil
	}
	out := make([]ProviderSummary, 0, len(s.validProviders))
	for name := range s.validProviders {
		out = append(out, ProviderSummary{Name: name, Role: "llm", Phase: "Ready"})
	}
	return out, nil
}

func (s *simulatedClient) ListToolRegistries(_ context.Context) ([]ToolRegistrySummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.listToolRegistriesErr != nil {
		return nil, s.listToolRegistriesErr
	}
	return s.toolRegistries, nil
}

func (s *simulatedClient) ValidateSkillSource(ctx context.Context, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.validSkillSources[name] {
		return nil
	}
	return fmt.Errorf("skillsource %q not found", name)
}

func (s *simulatedClient) Health(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.healthy {
		return nil
	}
	return fmt.Errorf("health check failed")
}

// matchesSelector is a simplified label selector matcher for testing.
// It supports comma-separated key=value pairs.
func matchesSelector(labels map[string]string, selector string) bool {
	if selector == "" {
		return true
	}
	for _, part := range strings.Split(selector, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if labels[kv[0]] != kv[1] {
			return false
		}
	}
	return true
}

// newSimulatedClientFactory returns an omniaClientFactory that always
// returns the provided simulatedClient.
func newSimulatedClientFactory(client *simulatedClient) omniaClientFactory {
	return func(cfg *Config) (omniaClient, error) {
		return client, nil
	}
}
