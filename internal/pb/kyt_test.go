package kytpb

import (
	"testing"
)

func TestScreenRequestGettersOnNil(t *testing.T) {
	var x *ScreenRequest
	if got := x.GetTxId(); got != "" {
		t.Errorf("GetTxId nil = %q", got)
	}
	if got := x.GetAddress(); got != "" {
		t.Errorf("GetAddress nil = %q", got)
	}
	if got := x.GetSourceAddress(); got != "" {
		t.Errorf("GetSourceAddress nil = %q", got)
	}
	if got := x.GetChain(); got != "" {
		t.Errorf("GetChain nil = %q", got)
	}
	if got := x.GetAmount(); got != "" {
		t.Errorf("GetAmount nil = %q", got)
	}
}

func TestScreenRequestGettersOnValue(t *testing.T) {
	x := &ScreenRequest{
		TxId:          "tx1",
		Address:       "0x1",
		SourceAddress: "0xsrc",
		Chain:         "ethereum",
		Amount:        "100",
	}
	if got := x.GetTxId(); got != "tx1" {
		t.Errorf("GetTxId = %q", got)
	}
	if got := x.GetAddress(); got != "0x1" {
		t.Errorf("GetAddress = %q", got)
	}
	if got := x.GetSourceAddress(); got != "0xsrc" {
		t.Errorf("GetSourceAddress = %q", got)
	}
	if got := x.GetChain(); got != "ethereum" {
		t.Errorf("GetChain = %q", got)
	}
	if got := x.GetAmount(); got != "100" {
		t.Errorf("GetAmount = %q", got)
	}
}

func TestScreenResponseGettersOnNil(t *testing.T) {
	var x *ScreenResponse
	if got := x.GetScreenId(); got != "" {
		t.Errorf("GetScreenId nil = %q", got)
	}
	if got := x.GetRiskScore(); got != 0 {
		t.Errorf("GetRiskScore nil = %d", got)
	}
	if got := x.GetExposure(); got != "" {
		t.Errorf("GetExposure nil = %q", got)
	}
	if got := x.GetDecision(); got != "" {
		t.Errorf("GetDecision nil = %q", got)
	}
	if got := x.GetVendor(); got != "" {
		t.Errorf("GetVendor nil = %q", got)
	}
	if got := x.GetCacheHit(); got != false {
		t.Errorf("GetCacheHit nil = %v", got)
	}
}

func TestScreenResponseGettersOnValue(t *testing.T) {
	x := &ScreenResponse{
		ScreenId:  "scr-1",
		RiskScore: 42,
		Exposure:  "HIGH_RISK",
		Decision:  "MANUAL_REVIEW",
		Vendor:    "chainalysis",
		CacheHit:  true,
	}
	if got := x.GetScreenId(); got != "scr-1" {
		t.Errorf("GetScreenId = %q", got)
	}
	if got := x.GetRiskScore(); got != 42 {
		t.Errorf("GetRiskScore = %d", got)
	}
	if got := x.GetExposure(); got != "HIGH_RISK" {
		t.Errorf("GetExposure = %q", got)
	}
	if got := x.GetDecision(); got != "MANUAL_REVIEW" {
		t.Errorf("GetDecision = %q", got)
	}
	if got := x.GetVendor(); got != "chainalysis" {
		t.Errorf("GetVendor = %q", got)
	}
	if got := x.GetCacheHit(); got != true {
		t.Errorf("GetCacheHit = %v", got)
	}
}

func TestGetAlertRequestGetters(t *testing.T) {
	var nilReq *GetAlertRequest
	if got := nilReq.GetId(); got != "" {
		t.Errorf("nil GetId = %q", got)
	}
	x := &GetAlertRequest{Id: "a1"}
	if got := x.GetId(); got != "a1" {
		t.Errorf("GetId = %q", got)
	}
}

func TestAlertGettersOnNil(t *testing.T) {
	var x *Alert
	for _, c := range []struct {
		name string
		got  string
	}{
		{"Id", x.GetId()},
		{"ScreenId", x.GetScreenId()},
		{"TxId", x.GetTxId()},
		{"Address", x.GetAddress()},
		{"Chain", x.GetChain()},
		{"Exposure", x.GetExposure()},
		{"Severity", x.GetSeverity()},
		{"Status", x.GetStatus()},
		{"Assignee", x.GetAssignee()},
		{"CreatedAt", x.GetCreatedAt()},
		{"ClosedAt", x.GetClosedAt()},
	} {
		if c.got != "" {
			t.Errorf("nil %s = %q", c.name, c.got)
		}
	}
}

func TestAlertGettersOnValue(t *testing.T) {
	x := &Alert{
		Id:        "a1",
		ScreenId:  "s1",
		TxId:      "tx1",
		Address:   "0x1",
		Chain:     "ethereum",
		Exposure:  "SANCTIONED",
		Severity:  "critical",
		Status:    "OPEN",
		Assignee:  "analyst1",
		CreatedAt: "2024-01-01T00:00:00Z",
		ClosedAt: "2024-01-02T00:00:00Z",
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"Id", x.GetId(), "a1"},
		{"ScreenId", x.GetScreenId(), "s1"},
		{"TxId", x.GetTxId(), "tx1"},
		{"Address", x.GetAddress(), "0x1"},
		{"Chain", x.GetChain(), "ethereum"},
		{"Exposure", x.GetExposure(), "SANCTIONED"},
		{"Severity", x.GetSeverity(), "critical"},
		{"Status", x.GetStatus(), "OPEN"},
		{"Assignee", x.GetAssignee(), "analyst1"},
		{"CreatedAt", x.GetCreatedAt(), "2024-01-01T00:00:00Z"},
		{"ClosedAt", x.GetClosedAt(), "2024-01-02T00:00:00Z"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q want %q", c.name, c.got, c.want)
		}
	}
}

func TestMessageStringAndProtoMessage(t *testing.T) {
	msgs := []struct {
		s string
		m func()
	}{
		{(&ScreenRequest{TxId: "tx1"}).String(), (&ScreenRequest{}).ProtoMessage},
		{(&ScreenResponse{ScreenId: "s"}).String(), (&ScreenResponse{}).ProtoMessage},
		{(&GetAlertRequest{Id: "a"}).String(), (&GetAlertRequest{}).ProtoMessage},
		{(&Alert{Id: "a"}).String(), (&Alert{}).ProtoMessage},
	}
	for i, msg := range msgs {
		if msg.s == "" {
			t.Errorf("msg %d String() empty", i)
		}
		msg.m()
	}
}

func TestMessageDescriptor(t *testing.T) {
	desc, _ := (&ScreenRequest{}).Descriptor()
	if len(desc) == 0 {
		t.Error("ScreenRequest Descriptor empty")
	}
	desc, _ = (&ScreenResponse{}).Descriptor()
	if len(desc) == 0 {
		t.Error("ScreenResponse Descriptor empty")
	}
	desc, _ = (&GetAlertRequest{}).Descriptor()
	if len(desc) == 0 {
		t.Error("GetAlertRequest Descriptor empty")
	}
	desc, _ = (&Alert{}).Descriptor()
	if len(desc) == 0 {
		t.Error("Alert Descriptor empty")
	}
}

func TestResetClearsFields(t *testing.T) {
	x := &ScreenRequest{TxId: "tx1", Address: "0x1"}
	x.Reset()
	if x.GetTxId() != "" || x.GetAddress() != "" {
		t.Errorf("Reset did not clear: %+v", x)
	}
}

func TestUnimplementedServerReturnsUnimplemented(t *testing.T) {
	u := UnimplementedKYTServiceServer{}
	if _, err := u.Screen(nil, nil); err == nil {
		t.Error("expected error from unimplemented Screen")
	}
	if _, err := u.GetAlert(nil, nil); err == nil {
		t.Error("expected error from unimplemented GetAlert")
	}
	u.mustEmbedUnimplementedKYTServiceServer()
	u.testEmbeddedByValue()
}