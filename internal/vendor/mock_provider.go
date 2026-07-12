package vendor

import (
	"context"
	"fmt"
	"sync"
)

// MockResponse presets a MockProvider response for a given (address, chain).
type MockResponse struct {
	Address     string
	Chain       string
	RiskScore   int
	Exposure    string
	Err         error
}

// MockProvider is a ScreenProvider used in tests. It returns preset responses
// keyed on (address, chain), or a canned clean response if no preset matches.
type MockProvider struct {
	mu        sync.Mutex
	name      string
	responses map[string]MockResponse
	calls     int
	failNext  int
}

// NewMockProvider returns a MockProvider.
func NewMockProvider(name string) *MockProvider {
	return &MockProvider{name: name, responses: make(map[string]MockResponse)}
}

// Name returns the provider name.
func (m *MockProvider) Name() string { return m.name }

// SetResponse presets the response for (address, chain).
func (m *MockProvider) SetResponse(addr, chain string, r MockResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r.Address = addr
	r.Chain = chain
	m.responses[addr+"|"+chain] = r
}

// FailNextN forces the next n calls to return ErrVendorUnavailable regardless
// of presets.
func (m *MockProvider) FailNextN(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failNext = n
}

// Calls returns the total number of Screen calls received.
func (m *MockProvider) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// Screen returns the preset response or a default clean verdict.
func (m *MockProvider) Screen(ctx context.Context, req ScreenRequest) (ScreenResponse, error) {
	m.mu.Lock()
	m.calls++
	if m.failNext > 0 {
		m.failNext--
		m.mu.Unlock()
		return ScreenResponse{}, ErrVendorUnavailable
	}
	r, ok := m.responses[req.Address+"|"+req.Chain]
	m.mu.Unlock()

	if !ok {
		// Default: clean verdict.
		return ScreenResponse{
			Vendor:    m.name,
			RiskScore: 0,
			Exposure:  "clean",
		}, nil
	}
	if r.Err != nil {
		return ScreenResponse{}, r.Err
	}
	return ScreenResponse{
		Vendor:    m.name,
		RiskScore: r.RiskScore,
		Exposure:  r.Exposure,
	}, nil
}

// String returns a debug representation.
func (m *MockProvider) String() string {
	return fmt.Sprintf("mock(%s,calls=%d)", m.name, m.Calls())
}