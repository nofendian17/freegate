package warp

import (
	"reflect"
	"testing"
)

func TestEndpointPreference(t *testing.T) {
	endpoints := []string{
		"[2606:4700:d0::a29f:c001]:2408",
		"162.159.192.1:2408",
		"[2606:4700:d0::a29f:c002]:2408",
		"162.159.193.1:2408",
		"engage.cloudflareclient.com:2408", // hostname, kept out by both helpers
	}
	wantIPv4 := []string{
		"162.159.192.1:2408",
		"162.159.193.1:2408",
		"[2606:4700:d0::a29f:c001]:2408",
		"[2606:4700:d0::a29f:c002]:2408",
	}
	wantIPv6 := []string{
		"[2606:4700:d0::a29f:c001]:2408",
		"[2606:4700:d0::a29f:c002]:2408",
		"162.159.192.1:2408",
		"162.159.193.1:2408",
	}
	if got := preferIPv4Endpoints(endpoints); !reflect.DeepEqual(got, wantIPv4) {
		t.Errorf("preferIPv4Endpoints = %v, want %v", got, wantIPv4)
	}
	if got := preferIPv6Endpoints(endpoints); !reflect.DeepEqual(got, wantIPv6) {
		t.Errorf("preferIPv6Endpoints = %v, want %v", got, wantIPv6)
	}
}
