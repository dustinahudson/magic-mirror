package provision

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Real `iw dev wlan0 scan` output, trimmed to three networks — including a
// duplicate SSID from a mesh, which is the case that makes a naive parser
// list the same network several times.
const scanFixture = `
BSS 11:22:33:44:55:66(on wlan0)
	TSF: 123456789 usec
	freq: 2437
	signal: -42.00 dBm
	capability: ESS Privacy ShortPreamble (0x0431)
	SSID: HomeNetwork
	RSN:	 * Version: 1
BSS aa:bb:cc:dd:ee:ff(on wlan0)
	freq: 5180
	signal: -71.00 dBm
	capability: ESS ShortSlotTime (0x0421)
	SSID: HomeNetwork
	RSN:	 * Version: 1
BSS 99:88:77:66:55:44(on wlan0)
	freq: 2412
	signal: -55.00 dBm
	capability: ESS ShortSlotTime (0x0401)
	SSID: CoffeeShopGuest
`

func TestParseScanDeduplicatesAndSorts(t *testing.T) {
	nets := parseScan(scanFixture)

	if len(nets) != 2 {
		t.Fatalf("got %d networks, want 2 (mesh SSID should collapse): %+v", len(nets), nets)
	}
	if nets[0].SSID != "HomeNetwork" {
		t.Errorf("strongest network is %q, want HomeNetwork", nets[0].SSID)
	}
	if nets[0].Signal != -42 {
		t.Errorf("kept signal %d for HomeNetwork, want the stronger -42", nets[0].Signal)
	}
	if !nets[0].Secure {
		t.Error("HomeNetwork should be marked secure (has RSN)")
	}
	if nets[1].SSID != "CoffeeShopGuest" {
		t.Errorf("second network is %q", nets[1].SSID)
	}
}

func testPortal(t *testing.T) *Portal {
	t.Helper()
	dir := t.TempDir()
	return &Portal{
		Interface:   "wlan0",
		SSID:        DefaultSSID,
		WPAConfPath: filepath.Join(dir, "wpa_supplicant.conf"),
		RunDir:      filepath.Join(dir, "run"),
		Runner:      ExecRunner{},
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestSaveCredentialsWritesValidConfig(t *testing.T) {
	p := testPortal(t)

	if err := p.SaveCredentials(context.Background(), "HomeNetwork", "hunter2hunter2", "GB"); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	body, err := os.ReadFile(p.WPAConfPath)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)

	for _, want := range []string{
		`country=GB`,
		`ssid="HomeNetwork"`,
		`psk="hunter2hunter2"`,
		`key_mgmt=WPA-PSK`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("config missing %q:\n%s", want, got)
		}
	}

	// The file holds a network password, so it must not be world-readable.
	info, err := os.Stat(p.WPAConfPath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("permissions are %o, want 600", perm)
	}
}

func TestSaveCredentialsOpenNetwork(t *testing.T) {
	p := testPortal(t)

	if err := p.SaveCredentials(context.Background(), "CoffeeShopGuest", "", ""); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	body, _ := os.ReadFile(p.WPAConfPath)
	got := string(body)
	if !strings.Contains(got, "key_mgmt=NONE") {
		t.Errorf("open network should use key_mgmt=NONE:\n%s", got)
	}
	if strings.Contains(got, "psk=") {
		t.Errorf("open network should have no psk:\n%s", got)
	}
}

// A rejected password must not damage a working config. Getting this wrong
// means one typo in the portal bricks the network connection.
func TestRejectedPasswordLeavesExistingConfigIntact(t *testing.T) {
	p := testPortal(t)

	original := "country=US\nnetwork={\n\tssid=\"Working\"\n}\n"
	if err := os.WriteFile(p.WPAConfPath, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := p.SaveCredentials(context.Background(), "Whatever", "short", ""); err == nil {
		t.Fatal("accepted a 5-character WPA password")
	}

	body, _ := os.ReadFile(p.WPAConfPath)
	if string(body) != original {
		t.Errorf("existing config was modified by a rejected save:\n%s", body)
	}
}

func TestEmptySSIDRejected(t *testing.T) {
	p := testPortal(t)
	if err := p.SaveCredentials(context.Background(), "   ", "password123", ""); err == nil {
		t.Fatal("accepted an empty SSID")
	}
}

// SSIDs can contain quotes and backslashes; %q escaping must keep the
// generated config parseable rather than letting one break out of the string.
func TestSSIDWithQuotesIsEscaped(t *testing.T) {
	p := testPortal(t)

	if err := p.SaveCredentials(context.Background(), `My "Home" \ Net`, "password123", ""); err != nil {
		t.Fatalf("SaveCredentials: %v", err)
	}

	body, _ := os.ReadFile(p.WPAConfPath)
	got := string(body)
	if !strings.Contains(got, `ssid="My \"Home\" \\ Net"`) {
		t.Errorf("SSID was not escaped:\n%s", got)
	}
}
