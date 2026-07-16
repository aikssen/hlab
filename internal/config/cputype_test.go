package config

import "testing"

func hasValue(choices []CPUChoice, v string) bool {
	for _, c := range choices {
		if c.Value == v {
			return true
		}
	}
	return false
}

func TestCPUTypeChoices(t *testing.T) {
	// A model is a promise about the instruction set the host will present, so
	// offering the other vendor's models would offer VMs that cannot start.
	t.Run("amd is offered EPYC and never an Intel model", func(t *testing.T) {
		got := CPUTypeChoices("AuthenticAMD")
		if !hasValue(got, "EPYC") {
			t.Error("AuthenticAMD choices lack EPYC")
		}
		if hasValue(got, "Westmere") {
			t.Error("AuthenticAMD choices include the Intel model Westmere")
		}
	})

	t.Run("intel is offered Westmere and never an AMD model", func(t *testing.T) {
		got := CPUTypeChoices("GenuineIntel")
		if !hasValue(got, "Westmere") {
			t.Error("GenuineIntel choices lack Westmere")
		}
		if hasValue(got, "EPYC") {
			t.Error("GenuineIntel choices include the AMD model EPYC")
		}
	})

	// The vendor is best-effort (an unreadable /nodes/<n>/status yields ""), and a
	// vendor hlab doesn't know must still produce a usable list — just without the
	// vendor-specific entry.
	t.Run("unknown vendor still offers the portable models", func(t *testing.T) {
		for _, v := range []string{"", "SomeFutureVendor"} {
			got := CPUTypeChoices(v)
			if !hasValue(got, DefaultCPUType) || !hasValue(got, "host") {
				t.Errorf("CPUTypeChoices(%q) is missing the vendor-neutral models", v)
			}
			if hasValue(got, "EPYC") || hasValue(got, "Westmere") {
				t.Errorf("CPUTypeChoices(%q) offered a vendor-specific model", v)
			}
		}
	})

	t.Run("the default is offered first, so it is preselected", func(t *testing.T) {
		for _, v := range []string{"AuthenticAMD", "GenuineIntel", ""} {
			if got := CPUTypeChoices(v); got[0].Value != DefaultCPUType {
				t.Errorf("CPUTypeChoices(%q)[0] = %q, want %q", v, got[0].Value, DefaultCPUType)
			}
		}
	})

	t.Run("every choice is labelled and explains its trade-off", func(t *testing.T) {
		for _, v := range []string{"AuthenticAMD", "GenuineIntel", ""} {
			for _, c := range CPUTypeChoices(v) {
				if c.Value == "" || c.Label == "" || c.Desc == "" {
					t.Errorf("CPUTypeChoices(%q) has an incomplete choice: %+v", v, c)
				}
			}
		}
	})
}

func TestCPUTypeOrDefault(t *testing.T) {
	// An unset cpu_type is not "no CPU model" — it means Terraform's default is what
	// the guest actually gets, so that is what a display must say.
	if got := CPUTypeOrDefault(""); got != DefaultCPUType {
		t.Errorf("CPUTypeOrDefault(\"\") = %q, want %q", got, DefaultCPUType)
	}
	if got := CPUTypeOrDefault("EPYC"); got != "EPYC" {
		t.Errorf("CPUTypeOrDefault(\"EPYC\") = %q, want EPYC", got)
	}
}
