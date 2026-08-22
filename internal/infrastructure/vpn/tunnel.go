package vpn

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/davegallant/vpngate/pkg/vpn"
)

func openVPNCandidatesForOS(goos string) []string {
	switch goos {
	case "darwin":
		return []string{"openvpn", "/opt/homebrew/bin/openvpn", "/usr/local/bin/openvpn"}
	case "windows":
		return []string{"openvpn.exe", "openvpn"}
	default:
		return []string{"openvpn"}
	}
}

func findOpenVPN() (string, error) {
	for _, bin := range openVPNCandidatesForOS(runtime.GOOS) {
		if p, err := execLookPath(bin); err == nil {
			return p, nil
		}
	}
	return "", exec.ErrNotFound
}

func installHintForOS(goos string) string {
	switch goos {
	case "darwin":
		return "brew install openvpn"
	case "windows":
		return "winget install OpenVPNTechnologies.OpenVPN  (or choco install openvpn)"
	default:
		return "sudo apt install openvpn  (or sudo yum install openvpn / sudo pacman -S openvpn)"
	}
}

func startOpenVPN(server vpn.Server) (*exec.Cmd, string, error) {
	decoded, err := base64.StdEncoding.DecodeString(server.OpenVpnConfigData)
	if err != nil {
		return nil, "", fmt.Errorf("decode openvpn config: %w", err)
	}
	tmp, err := os.CreateTemp("", "fg-vpngate-*.ovpn")
	if err != nil {
		return nil, "", fmt.Errorf("create config file: %w", err)
	}
	if _, err := tmp.Write(decoded); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("write config file: %w", err)
	}
	tmp.Close()
	cmd := exec.Command("openvpn", "--verb", "3", "--config", tmp.Name(), "--data-ciphers", "AES-128-CBC", "--pull-filter", "ignore", "dhcp-option", "--connect-retry", "2", "--connect-retry-max", "5")
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("start openvpn: %w", err)
	}
	return cmd, tmp.Name(), nil
}

func killProcess(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
}

func waitTunDown() {
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := net.InterfaceByName("tun0"); err != nil {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func waitTunnelUp(cmd *exec.Cmd) (string, error) {
	deadline := time.Now().Add(tunnelWaitTimeout)
	for {
		if iface, err := net.InterfaceByName("tun0"); err == nil && iface.Flags&net.FlagUp != 0 {
			break
		}
		if cmd != nil && cmd.Process != nil {
			if cmd.ProcessState != nil && cmd.ProcessState.Exited() {
				return "", fmt.Errorf("openvpn exited before tun0 was up")
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("openvpn tunnel (tun0) did not come up within %s", tunnelWaitTimeout)
		}
		time.Sleep(500 * time.Millisecond)
	}
	deadline2 := time.Now().Add(ipCheckTimeout)
	for {
		if ip, err := fetchPublicIP(); err == nil && ip != "" {
			return ip, nil
		}
		if time.Now().After(deadline2) {
			return "", nil
		}
		time.Sleep(750 * time.Millisecond)
	}
}

func fetchPublicIP() (string, error) {
	for _, u := range []string{"https://api.ipify.org?format=json", "https://ifconfig.me/ip"} {
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Get(u)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}
		if u == "https://api.ipify.org?format=json" {
			var out map[string]any
			if err := parseJSON(body, &out); err == nil {
				if ip, ok := out["ip"].(string); ok && strings.TrimSpace(ip) != "" {
					return strings.TrimSpace(ip), nil
				}
			}
		}
		if ip := strings.TrimSpace(string(body)); ip != "" {
			return ip, nil
		}
	}
	return "", errors.New("no ip")
}

func parseJSON(b []byte, v any) error {
	return json.Unmarshal(b, v)
}
