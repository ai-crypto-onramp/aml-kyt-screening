package vendor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPProvider is a ScreenProvider backed by an HTTP API. It is generic enough
// to back both Chainalysis and TRM by injecting a RequestEncoder and
// ResponseDecoder.
type HTTPProvider struct {
	name    string
	apiKey  string
	baseURL string
	client  *http.Client
	encoder RequestEncoder
	decoder ResponseDecoder
	breaker *CircuitBreaker
}

// RequestEncoder turns a ScreenRequest into an HTTP request body for the vendor.
type RequestEncoder func(req ScreenRequest) ([]byte, error)

// ResponseDecoder turns the vendor HTTP response body into a ScreenResponse.
type ResponseDecoder func(vendor string, req ScreenRequest, raw []byte) (ScreenResponse, error)

// NewHTTPProvider constructs an HTTPProvider.
func NewHTTPProvider(name, apiKey, baseURL string, timeout time.Duration, enc RequestEncoder, dec ResponseDecoder, breaker *CircuitBreaker) *HTTPProvider {
	return &HTTPProvider{
		name:    name,
		apiKey:  apiKey,
		baseURL: baseURL,
		client:  &http.Client{Timeout: timeout},
		encoder: enc,
		decoder: dec,
		breaker: breaker,
	}
}

// Name returns the vendor name.
func (p *HTTPProvider) Name() string { return p.name }

// Screen calls the vendor API with circuit-breaker and timeout protection.
func (p *HTTPProvider) Screen(ctx context.Context, req ScreenRequest) (ScreenResponse, error) {
	if p.apiKey == "" {
		return ScreenResponse{}, fmt.Errorf("vendor %s: missing api key", p.name)
	}
	if req.IdempotencyKey == "" {
		req.IdempotencyKey = IdempotencyKey(req.TxID, req.Address, req.Chain)
	}

	body, err := p.encoder(req)
	if err != nil {
		return ScreenResponse{}, fmt.Errorf("encode: %w", err)
	}

	var resp ScreenResponse
	callErr := p.breaker.Execute(ctx, func(ctx context.Context) error {
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/screen", bytes.NewReader(body))
		if err != nil {
			return err
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+p.apiKey)
		httpReq.Header.Set("X-Idempotency-Key", req.IdempotencyKey)

		httpResp, err := p.client.Do(httpReq)
		if err != nil {
			return err
		}
		defer httpResp.Body.Close()
		raw, err := io.ReadAll(httpResp.Body)
		if err != nil {
			return err
		}
		if httpResp.StatusCode >= 500 {
			return fmt.Errorf("vendor %s: status %d", p.name, httpResp.StatusCode)
		}
		if httpResp.StatusCode >= 400 {
			return fmt.Errorf("vendor %s: client error status %d: %s", p.name, httpResp.StatusCode, string(raw))
		}
		out, err := p.decoder(p.name, req, raw)
		if err != nil {
			return err
		}
		out.RawRequest = body
		out.RawResponse = raw
		resp = out
		return nil
	})
	if callErr != nil {
		if errors.Is(callErr, ErrCircuitOpen) {
			return ScreenResponse{}, ErrVendorUnavailable
		}
		return ScreenResponse{}, callErr
	}
	return resp, nil
}

// ChainalysisEncoder encodes a ScreenRequest in Chainalysis' API shape.
func ChainalysisEncoder(req ScreenRequest) ([]byte, error) {
	return json.Marshal(map[string]any{
		"address":      req.Address,
		"chain":        req.Chain,
		"amount":       req.Amount,
		"tx_id":        req.TxID,
		"source":       req.SourceAddress,
		"idempotency":  req.IdempotencyKey,
	})
}

// ChainalysisDecoder decodes a Chainalysis API response.
func ChainalysisDecoder(vendor string, req ScreenRequest, raw []byte) (ScreenResponse, error) {
	var resp struct {
		RiskScore int    `json:"risk_score"`
		Exposure  string `json:"exposure"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ScreenResponse{}, fmt.Errorf("decode chainalysis: %w", err)
	}
	return ScreenResponse{
		Vendor:    vendor,
		RiskScore: resp.RiskScore,
		Exposure:  resp.Exposure,
	}, nil
}

// TRMEncoder encodes a ScreenRequest in TRM Labs' API shape.
func TRMEncoder(req ScreenRequest) ([]byte, error) {
	return json.Marshal(map[string]any{
		"address":     req.Address,
		"chain":       req.Chain,
		"amount":      req.Amount,
		"txId":        req.TxID,
		"source":      req.SourceAddress,
		"idempotencyKey": req.IdempotencyKey,
	})
}

// TRMDecoder decodes a TRM Labs API response.
func TRMDecoder(vendor string, req ScreenRequest, raw []byte) (ScreenResponse, error) {
	var resp struct {
		RiskScore int    `json:"riskScore"`
		Exposure  string `json:"exposure"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return ScreenResponse{}, fmt.Errorf("decode trm: %w", err)
	}
	return ScreenResponse{
		Vendor:    vendor,
		RiskScore: resp.RiskScore,
		Exposure:  resp.Exposure,
	}, nil
}