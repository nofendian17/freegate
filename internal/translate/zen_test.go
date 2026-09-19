package translate

import "testing"

func TestOpenCodeSessionRE(t *testing.T) {
	for _, id := range []string{
		"ses_0afae3e4c001AmMPIe8RFqNeTF",
		"ses_f5051c1b3ffeIKElIsuBAg4ZSs",
	} {
		if !OpenCodeSessionRE.MatchString(id) {
			t.Errorf("expected canonical %q", id)
		}
	}
	for _, id := range []string{
		"",
		"ses_F5051C1B3FFEIKElIsuBAg4ZSs",
		"ses_short",
		"msg_0afae3e4c001AmMPIe8RFqNeTF",
		"claude:abc-123",
		"ses_0afae3e4c001AmMPIe8RFqNeTF!",
	} {
		if OpenCodeSessionRE.MatchString(id) {
			t.Errorf("expected non-canonical %q", id)
		}
	}
}
