package lan

import (
	"strings"
	"testing"
)

func TestWriteReadDNSNameRoundTrip(t *testing.T) {
	for _, name := range []string{"_novelstack._tcp.local.", "NovelStack-8080._novelstack._tcp.local.", "192-168-1-5.local."} {
		buf := make([]byte, 0, 128)
		writeDNSName(&buf, name)
		got, _, err := readDNSName(buf, 0)
		if err != nil {
			t.Fatalf("read %q: %v", name, err)
		}
		// DNS 名称不区分大小写，读写两侧都按小写规范化。
		if got != strings.ToLower(name) {
			t.Fatalf("round trip = %q, want %q", got, strings.ToLower(name))
		}
	}
}

func TestParseDNSQuestions(t *testing.T) {
	buf := make([]byte, 0, 128)
	buf = append(buf, 0, 0, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0) // header, QDCOUNT=1
	writeDNSName(&buf, dnsServiceType)
	buf = append(buf, 0, byte(dnsTypePTR), 0, byte(dnsClassIN))
	qs, err := parseDNSQuestions(buf)
	if err != nil {
		t.Fatal(err)
	}
	if len(qs) != 1 || qs[0].name != dnsServiceType || qs[0].qtype != dnsTypePTR {
		t.Fatalf("questions = %+v", qs)
	}
}

func TestBuildMDNSResponse(t *testing.T) {
	resp, err := buildMDNSResponse(0x1234, "NovelStack-8080", "192-168-1-5",
		[]byte{192, 168, 1, 5}, 8080, []string{"path=/", "proto=http"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp) < 12 {
		t.Fatalf("response too short: %d", len(resp))
	}
	if resp[0] != 0x12 || resp[1] != 0x34 {
		t.Fatalf("ID mismatch: %x %x", resp[0], resp[1])
	}
	if resp[2]&0x80 == 0 {
		t.Fatal("QR flag not set")
	}
	// ANCOUNT = 4 (PTR + SRV + TXT + A)
	if got := int(resp[6])<<8 | int(resp[7]); got != 4 {
		t.Fatalf("ANCOUNT = %d, want 4", got)
	}
	// 应答里必须包含 IPv4 字节序列与端口 8080 (0x1F90)。
	foundIP, foundPort := false, false
	for i := 0; i+4 <= len(resp); i++ {
		if resp[i] == 192 && resp[i+1] == 168 && resp[i+2] == 1 && resp[i+3] == 5 {
			foundIP = true
		}
		if resp[i] == 0x1F && resp[i+1] == 0x90 {
			foundPort = true
		}
	}
	if !foundIP {
		t.Fatal("A record IPv4 not found")
	}
	if !foundPort {
		t.Fatal("SRV port not found")
	}
}

func TestIsSixDigits(t *testing.T) {
	for _, c := range []string{"000000", "123456", "999999"} {
		if !isSixDigits(c) {
			t.Fatalf("%q should be valid", c)
		}
	}
	for _, c := range []string{"", "12345", "1234567", "abcdef", " 12345"} {
		if isSixDigits(c) {
			t.Fatalf("%q should be invalid", c)
		}
	}
}
