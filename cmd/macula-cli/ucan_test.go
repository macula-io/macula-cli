package main

import (
	"testing"

	"github.com/macula-io/macula-go/ucan"
)

// A -capability value is "with:can", split at the last colon: a with may be
// an MRI, which has colons of its own, and an ability has none.
func TestCapabilityFlagSplitsAtTheLastColon(t *testing.T) {
	cases := []struct {
		in   string
		want ucan.Capability
	}{
		{"mri:realm:net.beam-campus:member/email-verified",
			ucan.Capability{With: "mri:realm:net.beam-campus", Can: "member/email-verified"}},
		{"mri:proc:io.macula/acme/svc.do:call",
			ucan.Capability{With: "mri:proc:io.macula/acme/svc.do", Can: "call"}},
		{"orders:read", ucan.Capability{With: "orders", Can: "read"}},
	}
	for _, c := range cases {
		var caps capabilityFlag
		if err := caps.Set(c.in); err != nil {
			t.Errorf("Set(%q) = %v, want nil", c.in, err)
			continue
		}
		if len(caps) != 1 || caps[0] != c.want {
			t.Errorf("Set(%q) gave %+v, want [%+v]", c.in, []ucan.Capability(caps), c.want)
		}
	}
}

// A value with an empty with, an empty can, or no colon at all is refused,
// and adds nothing.
func TestCapabilityFlagRefusesAnEmptyWithOrCan(t *testing.T) {
	for _, in := range []string{":call", "mri:realm:net.beam-campus:", "call", ""} {
		var caps capabilityFlag
		if err := caps.Set(in); err == nil {
			t.Errorf("Set(%q) = nil, want an error", in)
		}
		if len(caps) != 0 {
			t.Errorf("Set(%q) added %+v, want nothing", in, []ucan.Capability(caps))
		}
	}
}

// validateCapability refuses an empty with, an empty can, and a can with a
// colon in it: an ability has no colon.
func TestValidateCapabilityRefusesAnEmptyWithAndAColonInCan(t *testing.T) {
	cases := []struct{ with, can string }{
		{"", "call"},
		{"mri:realm:net.beam-campus", ""},
		{"mri:realm:net.beam-campus", "member:admin"},
	}
	for _, c := range cases {
		if err := validateCapability(c.with, c.can); err == nil {
			t.Errorf("validateCapability(%q, %q) = nil, want an error", c.with, c.can)
		}
	}
	if err := validateCapability("mri:realm:net.beam-campus", "member/email-verified"); err != nil {
		t.Errorf("validateCapability of a well-formed capability = %v, want nil", err)
	}
}
