package config

// DefaultCPUType is the QEMU CPU model a VM gets when cpu_type is unset. It
// mirrors the default in assets/terraform/variables.tf — the empty string is what
// hlab stores, so Terraform's own default applies and an old declaration written
// before cpu_type existed renders exactly as before.
const DefaultCPUType = "x86-64-v2-AES"

// CPUChoice is one CPU model offered during setup.
type CPUChoice struct {
	// Value is the QEMU model name, or "" for "leave it to Terraform's default".
	Value string
	Label string
	// Desc is the trade-off, not a restatement of the name. Every choice here is a
	// balance between how many instructions the guest gets and whether it can still
	// live-migrate, and that is the only thing worth saying about it.
	Desc string
}

// CPUTypeChoices returns the CPU models offered for a host of the given vendor
// (Proxmox's cpuinfo.vendor: "AuthenticAMD", "GenuineIntel", anything else).
//
// This is a curated shortlist, not the ~103 models Proxmox reports: most are
// specific CPU generations nobody picks deliberately, and a third of them are for
// the other vendor and would not even start here. What actually matters is the
// axis they sit on — portability vs instruction set — so the list is one entry per
// meaningful point on it.
//
// The vendor-specific entry is the oldest model of that vendor that exposes
// PCLMULQDQ, since that is the practical reason to leave the portable default:
// x86-64-v2-AES gives AES but not PCLMULQDQ, and a binary compiled to require it
// dies at startup with SIGILL. Oldest, because a CPU model can only be presented
// by a host that supports it — picking the oldest keeps every node in the cluster
// eligible, which is what live migration needs.
func CPUTypeChoices(vendor string) []CPUChoice {
	choices := []CPUChoice{
		{
			Value: DefaultCPUType,
			Label: DefaultCPUType + " (default)",
			Desc:  "Portable across any host. Has AES but NOT PCLMULQDQ — some binaries abort.",
		},
		{
			Value: "x86-64-v3",
			Label: "x86-64-v3",
			Desc:  "Adds AVX2. Still no PCLMULQDQ. Needs a 2013-era host or newer.",
		},
		{
			Value: "x86-64-v4",
			Label: "x86-64-v4",
			Desc:  "Adds AVX-512. Still no PCLMULQDQ. Many CPUs, incl. most AMD, can't present it.",
		},
	}

	switch vendor {
	case "AuthenticAMD":
		choices = append(choices, CPUChoice{
			Value: "EPYC",
			Label: "EPYC",
			Desc:  "Has PCLMULQDQ. Oldest AMD model, so every AMD node can present it (migration-safe).",
		})
	case "GenuineIntel":
		choices = append(choices, CPUChoice{
			Value: "Westmere",
			Label: "Westmere",
			Desc:  "Has PCLMULQDQ. Oldest Intel model with it (2010), so any Intel node can present it.",
		})
	}

	return append(choices, CPUChoice{
		Value: "host",
		Label: "host",
		Desc:  "Every feature the host has, PCLMULQDQ included. Breaks live migration between unlike CPUs.",
	})
}

// CPUTypeOrDefault reports the effective model for display: the configured one, or
// the Terraform default that an empty value resolves to.
func CPUTypeOrDefault(cpuType string) string {
	if cpuType == "" {
		return DefaultCPUType
	}
	return cpuType
}
