package config

// DefaultCPUType is the QEMU CPU model a VM gets when cpu_type is unset. It
// mirrors the default in assets/terraform/variables.tf — the empty string is what
// hlab stores, so Terraform's own default applies and an old declaration written
// before cpu_type existed renders exactly as before.
const DefaultCPUType = "x86-64-v2-AES"

// CPUTypeChoices returns the CPU models offered during setup for a host of the
// given vendor (Proxmox's cpuinfo.vendor: "AuthenticAMD", "GenuineIntel", anything
// else), most portable first — so the first entry is the default.
//
// This is a curated shortlist, not the ~103 models Proxmox reports: most are
// specific CPU generations nobody picks deliberately, and a third of them are for
// the other vendor and would not even start here. What actually matters is the axis
// they sit on — portability vs instruction set — so the list is one entry per
// meaningful point on it.
//
// The vendor-specific entry is the oldest model of that vendor that exposes
// PCLMULQDQ, since that is the practical reason to leave the portable default:
// x86-64-v2-AES gives AES but not PCLMULQDQ, and a binary compiled to require it
// dies at startup with SIGILL. Oldest, because a CPU model can only be presented by
// a host that supports it — picking the oldest keeps every node in the cluster
// eligible, which is what live migration needs.
func CPUTypeChoices(vendor string) []string {
	choices := []string{DefaultCPUType, "x86-64-v3", "x86-64-v4"}
	switch vendor {
	case "AuthenticAMD":
		choices = append(choices, "EPYC")
	case "GenuineIntel":
		choices = append(choices, "Westmere")
	}
	// host exposes every feature the machine has, PCLMULQDQ included, at the cost of
	// live migration between unlike CPUs. Last: it is the escape hatch, not a
	// default.
	return append(choices, "host")
}

// CPUTypeOrDefault reports the effective model for display: the configured one, or
// the Terraform default that an empty value resolves to.
func CPUTypeOrDefault(cpuType string) string {
	if cpuType == "" {
		return DefaultCPUType
	}
	return cpuType
}
