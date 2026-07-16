package config

import (
	"slices"
	"testing"
)

func TestCPUTypeChoices(t *testing.T) {
	// A model is a promise about the instruction set the host will present, so
	// offering the other vendor's models would offer VMs that cannot start.
	t.Run("amd is offered EPYC and never an Intel model", func(t *testing.T) {
		got := CPUTypeChoices("AuthenticAMD")
		if !slices.Contains(got, "EPYC") {
			t.Errorf("AuthenticAMD choices lack EPYC: %v", got)
		}
		if slices.Contains(got, "Westmere") {
			t.Errorf("AuthenticAMD choices include the Intel model Westmere: %v", got)
		}
	})

	t.Run("intel is offered Westmere and never an AMD model", func(t *testing.T) {
		got := CPUTypeChoices("GenuineIntel")
		if !slices.Contains(got, "Westmere") {
			t.Errorf("GenuineIntel choices lack Westmere: %v", got)
		}
		if slices.Contains(got, "EPYC") {
			t.Errorf("GenuineIntel choices include the AMD model EPYC: %v", got)
		}
	})

	// The vendor is best-effort (an unreadable /nodes/<n>/status yields ""), and a
	// vendor hlab doesn't know must still produce a usable list — just without the
	// vendor-specific entry.
	t.Run("unknown vendor still offers the portable models", func(t *testing.T) {
		for _, v := range []string{"", "SomeFutureVendor"} {
			got := CPUTypeChoices(v)
			if !slices.Contains(got, DefaultCPUType) || !slices.Contains(got, "host") {
				t.Errorf("CPUTypeChoices(%q) = %v, missing the vendor-neutral models", v, got)
			}
			if slices.Contains(got, "EPYC") || slices.Contains(got, "Westmere") {
				t.Errorf("CPUTypeChoices(%q) = %v, offered a vendor-specific model", v, got)
			}
		}
	})

	// First is what the select lands on, so the default has to lead.
	t.Run("the default comes first", func(t *testing.T) {
		for _, v := range []string{"AuthenticAMD", "GenuineIntel", ""} {
			if got := CPUTypeChoices(v); got[0] != DefaultCPUType {
				t.Errorf("CPUTypeChoices(%q)[0] = %q, want %q", v, got[0], DefaultCPUType)
			}
		}
	})

	// host trades live migration away, so it must never be the one landed on.
	t.Run("host comes last", func(t *testing.T) {
		for _, v := range []string{"AuthenticAMD", "GenuineIntel", ""} {
			got := CPUTypeChoices(v)
			if got[len(got)-1] != "host" {
				t.Errorf("CPUTypeChoices(%q) last = %q, want host", v, got[len(got)-1])
			}
		}
	})

	t.Run("no empty or duplicate entries", func(t *testing.T) {
		for _, v := range []string{"AuthenticAMD", "GenuineIntel", ""} {
			got := CPUTypeChoices(v)
			seen := map[string]bool{}
			for _, c := range got {
				if c == "" {
					t.Errorf("CPUTypeChoices(%q) has an empty entry: %v", v, got)
				}
				if seen[c] {
					t.Errorf("CPUTypeChoices(%q) repeats %q: %v", v, c, got)
				}
				seen[c] = true
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
