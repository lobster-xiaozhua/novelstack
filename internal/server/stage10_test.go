package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"testing"
)

func TestLANPairingFlow(t *testing.T) {
	s := newStack(t)
	client := loginOK(t, s)

	// 1) 控制台生成 6 位短码。
	resp := doJSON(t, client, http.MethodPost, s.ts.URL+"/api/admin/lan/pairing", map[string]any{})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pairing create status = %d", resp.StatusCode)
	}
	var code struct {
		Code      string `json:"code"`
		ExpiresIn int    `json:"expires_in_s"`
		Endpoint  string `json:"endpoint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&code); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(code.Code) != 6 {
		t.Fatalf("code = %q, want 6 digits", code.Code)
	}
	if code.ExpiresIn <= 0 {
		t.Fatalf("expires_in = %d", code.ExpiresIn)
	}

	// 2) App 用短码换 token（无需 session）。
	pair := doJSON(t, http.DefaultClient, http.MethodPost, s.ts.URL+"/api/lan/pair", map[string]any{
		"pairing_code": code.Code,
		"device_name":  "LAN 测试设备",
		"public_key":   "",
	})
	if pair.StatusCode != http.StatusOK {
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(pair.Body)
		t.Fatalf("lan pair status = %d body = %s", pair.StatusCode, body.String())
	}
	var auth struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(pair.Body).Decode(&auth); err != nil {
		t.Fatal(err)
	}
	pair.Body.Close()
	if len(auth.Token) < 20 {
		t.Fatalf("token too short: %q", auth.Token)
	}

	// 3) 短码一次性：再次兑换必须失败。
	replay := doJSON(t, http.DefaultClient, http.MethodPost, s.ts.URL+"/api/lan/pair", map[string]any{
		"pairing_code": code.Code,
		"device_name":  "重放设备",
	})
	if replay.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay status = %d, want 401", replay.StatusCode)
	}
	replay.Body.Close()
}

func TestLANPairRejectsBadCode(t *testing.T) {
	s := newStack(t)
	for _, code := range []string{"000000", "12345", "abcdef"} {
		resp := doJSON(t, http.DefaultClient, http.MethodPost, s.ts.URL+"/api/lan/pair", map[string]any{
			"pairing_code": code,
			"device_name":  "设备",
		})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("code %q status = %d, want 401", code, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

func TestLANInfoNoAuth(t *testing.T) {
	s := newStack(t)
	resp, err := http.Get(s.ts.URL + "/api/lan/info")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("lan info status = %d", resp.StatusCode)
	}
	var info struct {
		Enabled bool   `json:"enabled"`
		Service string `json:"service"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if info.Service != "_novelstack._tcp" {
		t.Fatalf("service = %q", info.Service)
	}
}
