package daemon

import (
	"testing"

	"gocurl/internal/config"
)

func TestTransportRoundTripsDenyPrivateAddresses(t *testing.T) {
	o := config.NewOptions()
	o.DenyPrivateAddresses = true

	var transport daemonTransport
	transportFromOptions(&transport, o)
	if !transport.DenyPrivateAddresses {
		t.Fatal("deny_private_addresses missing from daemon transport")
	}

	got := optionsFromTransport(transport)
	if !got.DenyPrivateAddresses {
		t.Fatal("deny_private_addresses lost when rebuilding options")
	}
}

func TestTransportDefaultsToPermissive(t *testing.T) {
	o := config.NewOptions()

	var transport daemonTransport
	transportFromOptions(&transport, o)
	if transport.DenyPrivateAddresses {
		t.Fatal("deny_private_addresses must default to disabled")
	}
	if got := optionsFromTransport(transport); got.DenyPrivateAddresses {
		t.Fatal("deny_private_addresses enabled by default")
	}
}
