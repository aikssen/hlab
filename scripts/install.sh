#!/usr/bin/env bash
#
# hlab installer — idempotent, safe to re-run (it updates in place).
#
#   Local checkout:   ./scripts/install.sh          # builds from source
#   From a URL:       curl -fsSL <url>/install.sh | bash
#
# Resolution order:
#   1. If run from a local hlab checkout -> build from source (needs Go).
#   2. Else download the release binary for this OS/arch from GitHub.
#   3. Else fall back to `go install` (needs Go).
#
# hlab also bundles its two runtime tools — Terraform (required for
# `hlab {vm,ct} create`) and Ansible (only for `hlab {vm,ct} provision`). They are
# installed AFTER hlab and never block it: an already-present tool is reused, and a
# failed dependency install only warns.
#
# Env overrides:
#   HLAB_BIN_DIR         install dir (default: ~/.local/bin)
#   HLAB_VERSION         version/tag to install (default: latest)
#   HLAB_FROM_RELEASE=1  force the release/go-install path even inside a checkout
#   HLAB_TERRAFORM_VERSION  Terraform version to install (default: 1.15.7)
#   HLAB_SKIP_DEPS=1     skip installing Terraform/Ansible (manage your own toolchain)
#
set -euo pipefail

# Update REPO if the repository ever moves to a new owner/name.
REPO="aikssen/hlab"
BIN_DIR="${HLAB_BIN_DIR:-$HOME/.local/bin}"
VERSION="${HLAB_VERSION:-latest}"
# Terraform version this project is validated against; override with HLAB_TERRAFORM_VERSION.
TERRAFORM_DEFAULT_VERSION="1.15.7"

# Dependency-phase state, filled in by install_deps (for the final summary).
hlab_state="missing"
tf_state="missing"
ansible_state="missing"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$1"; }
warn() { printf '\033[1;33m!!\033[0m %s\n' "$1"; }
die()  { printf '\033[1;31mxx\033[0m %s\n' "$1" >&2; exit 1; }

mkdir -p "$BIN_DIR"

# OS / arch in the naming used by the release assets.
os="$(uname -s | tr '[:upper:]' '[:lower:]')"   # linux | darwin
arch="$(uname -m)"
case "$arch" in
  x86_64|amd64)  arch="amd64" ;;
  aarch64|arm64) arch="arm64" ;;
  *) die "unsupported architecture: $arch" ;;
esac

# Detect a local checkout relative to this script.
source_dir=""
if [ -n "${BASH_SOURCE:-}" ]; then
  d="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." 2>/dev/null && pwd || true)"
  if [ -n "$d" ] && [ -f "$d/go.mod" ] && grep -q "module github.com/aikssen/hlab" "$d/go.mod" 2>/dev/null; then
    source_dir="$d"
  fi
fi

build_from_source() {
  command -v go >/dev/null 2>&1 || return 1
  log "Building hlab from source ($source_dir)..."
  ( cd "$source_dir" && go build -o "$BIN_DIR/hlab" . )
}

# Print the SHA-256 of a file using whichever tool is available (macOS ships
# `shasum`, most Linux distros ship `sha256sum`). Empty output => no tool.
sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

download_release() {
  local base url asset sums_url
  if [ "$VERSION" = "latest" ]; then
    base="https://github.com/$REPO/releases/latest/download"
  else
    base="https://github.com/$REPO/releases/download/${VERSION}"
  fi
  asset="hlab_${os}_${arch}"
  url="${base}/${asset}"
  sums_url="${base}/SHA256SUMS"

  log "Downloading $url ..."
  curl -fsSL "$url" -o "$BIN_DIR/hlab.tmp" || return 1

  # Verify against SHA256SUMS from the same release. Older releases (and the
  # first-release window) may not have the file yet -> warn and continue.
  if curl -fsSL "$sums_url" -o "$BIN_DIR/SHA256SUMS.tmp" 2>/dev/null; then
    local want have
    want="$(awk -v a="$asset" '$2 == a || $2 == "*"a {print $1}' "$BIN_DIR/SHA256SUMS.tmp" | head -n1)"
    rm -f "$BIN_DIR/SHA256SUMS.tmp"
    if [ -z "$want" ]; then
      warn "SHA256SUMS has no entry for $asset — skipping checksum verification."
    else
      have="$(sha256_of "$BIN_DIR/hlab.tmp")"
      if [ -z "$have" ]; then
        warn "no sha256 tool (sha256sum/shasum) found — skipping checksum verification."
      elif [ "$have" != "$want" ]; then
        rm -f "$BIN_DIR/hlab.tmp"
        die "checksum mismatch for $asset (expected $want, got $have) — aborting."
      else
        log "Checksum verified ($asset)."
      fi
    fi
  else
    warn "SHA256SUMS not found for this release — skipping checksum verification."
  fi

  chmod +x "$BIN_DIR/hlab.tmp"
  mv -f "$BIN_DIR/hlab.tmp" "$BIN_DIR/hlab"
}

go_install() {
  command -v go >/dev/null 2>&1 || return 1
  log "Installing via 'go install' (@${VERSION})..."
  GOBIN="$BIN_DIR" go install "github.com/${REPO}@${VERSION}"
}

# --- runtime dependencies (Terraform + Ansible) -----------------------------
#
# Installed after hlab so a dependency hiccup can never block the primary binary.
# Every probe is guarded so `set -e` won't abort mid-way; a tool that is already
# present is reused, and an install that can't complete only warns.

# Terraform: reuse an existing binary, else download the official HashiCorp zip
# for this OS/arch and drop the `terraform` binary into $BIN_DIR.
install_terraform() {
  if command -v terraform >/dev/null 2>&1; then
    local v; v="$(terraform version 2>/dev/null | head -1 || true)"
    log "Terraform already installed (${v:-present}) — using it."
    tf_state="already-present"
    return 0
  fi

  local ver url tmp zip
  ver="${HLAB_TERRAFORM_VERSION:-$TERRAFORM_DEFAULT_VERSION}"

  if ! command -v curl >/dev/null 2>&1; then
    warn "curl not found — cannot auto-install Terraform. Install it manually: https://developer.hashicorp.com/terraform/install"
    return 0
  fi
  if ! command -v unzip >/dev/null 2>&1; then
    warn "unzip not found — cannot extract Terraform. Install unzip then re-run, or grab it manually: https://developer.hashicorp.com/terraform/install"
    return 0
  fi

  url="https://releases.hashicorp.com/terraform/${ver}/terraform_${ver}_${os}_${arch}.zip"
  tmp="$(mktemp -d 2>/dev/null || true)"
  if [ -z "$tmp" ]; then
    warn "could not create a temp dir for Terraform — skipping. Install manually: https://developer.hashicorp.com/terraform/install"
    return 0
  fi
  zip="$tmp/terraform.zip"

  log "Installing Terraform ${ver} ($os/$arch)..."
  if ! curl -fsSL "$url" -o "$zip"; then
    warn "Terraform download failed ($url) — install it manually: https://developer.hashicorp.com/terraform/install"
    rm -rf "$tmp"
    return 0
  fi
  # Extract only the binary (skip LICENSE.txt etc.).
  if ! unzip -oq "$zip" terraform -d "$tmp" 2>/dev/null; then
    warn "could not unzip Terraform — install it manually: https://developer.hashicorp.com/terraform/install"
    rm -rf "$tmp"
    return 0
  fi
  chmod +x "$tmp/terraform" 2>/dev/null || true
  if ! mv -f "$tmp/terraform" "$BIN_DIR/terraform"; then
    warn "could not install Terraform into $BIN_DIR — install it manually: https://developer.hashicorp.com/terraform/install"
    rm -rf "$tmp"
    return 0
  fi
  rm -rf "$tmp"

  if "$BIN_DIR/terraform" version >/dev/null 2>&1; then
    log "✓ Terraform installed to $BIN_DIR/terraform ($("$BIN_DIR/terraform" version 2>/dev/null | head -1))"
  else
    warn "Terraform installed to $BIN_DIR/terraform but 'terraform version' did not run cleanly — verify manually."
  fi
  tf_state="installed-now"
}

# Mark Ansible as freshly installed and best-effort report the version.
_ansible_installed() {
  ansible_state="installed-now"
  if command -v ansible-playbook >/dev/null 2>&1; then
    log "✓ Ansible installed ($(ansible-playbook --version 2>/dev/null | head -1))"
  else
    log "✓ Ansible installed (ensure $BIN_DIR / ~/.local/bin is on your PATH, then restart your shell)."
  fi
  return 0
}

# Ansible: reuse an existing ansible-playbook, else try a chain of installers,
# stopping at the first that succeeds. Only needed for `hlab {vm,ct} provision`.
install_ansible() {
  if command -v ansible-playbook >/dev/null 2>&1; then
    local v; v="$(ansible-playbook --version 2>/dev/null | head -1 || true)"
    log "Ansible already installed (${v:-present}) — using it."
    ansible_state="already-present"
    return 0
  fi

  log "Installing Ansible (best-effort)..."

  # 1. pipx (the project's documented approach).
  if command -v pipx >/dev/null 2>&1; then
    if pipx install --include-deps ansible >/dev/null 2>&1 || pipx install ansible-core >/dev/null 2>&1; then
      _ansible_installed
      return 0
    fi
  fi

  # 2. Homebrew on macOS.
  if [ "$os" = "darwin" ] && command -v brew >/dev/null 2>&1; then
    if brew install ansible >/dev/null 2>&1; then
      _ansible_installed
      return 0
    fi
  fi

  # 3. Linux system package manager — only non-interactively with sudo present.
  if [ "$os" = "linux" ] && command -v sudo >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1; then
      if sudo -n apt-get update >/dev/null 2>&1 && sudo -n apt-get install -y ansible >/dev/null 2>&1; then
        _ansible_installed; return 0
      fi
    elif command -v dnf >/dev/null 2>&1; then
      if sudo -n dnf install -y ansible >/dev/null 2>&1; then _ansible_installed; return 0; fi
    elif command -v pacman >/dev/null 2>&1; then
      if sudo -n pacman -Sy --noconfirm ansible >/dev/null 2>&1; then _ansible_installed; return 0; fi
    elif command -v zypper >/dev/null 2>&1; then
      if sudo -n zypper --non-interactive install ansible >/dev/null 2>&1; then _ansible_installed; return 0; fi
    fi
  fi

  # 4. pip3 --user (may be blocked by PEP 668 externally-managed environments).
  if command -v pip3 >/dev/null 2>&1; then
    if pip3 install --user ansible-core >/dev/null 2>&1; then
      _ansible_installed
      return 0
    fi
  fi

  warn "Could not auto-install Ansible — it is only needed for 'hlab {vm,ct} provision'."
  warn "Install it later with:  mise use -g pipx:ansible-core   (see https://docs.ansible.com/)"
  return 0
}

install_deps() {
  if [ "${HLAB_SKIP_DEPS:-0}" = "1" ]; then
    log "HLAB_SKIP_DEPS=1 — skipping Terraform/Ansible install."
    tf_state="skipped"
    ansible_state="skipped"
    return 0
  fi
  install_terraform
  install_ansible
}

summary() {
  log "Summary:"
  printf '   hlab:      %s\n' "$hlab_state"
  printf '   terraform: %s\n' "$tf_state"
  printf '   ansible:   %s\n' "$ansible_state"
}
# ----------------------------------------------------------------------------

if [ -n "$source_dir" ] && [ "${HLAB_FROM_RELEASE:-0}" != "1" ]; then
  build_from_source || die "build failed (is Go installed?)"
else
  download_release || go_install || die "could not install hlab (no release asset and Go not available)"
fi
hlab_state="installed-now"

log "✓ hlab installed to $BIN_DIR/hlab"
case ":$PATH:" in
  *":$BIN_DIR:"*) ;;
  *) warn "Add $BIN_DIR to your PATH (e.g. in ~/.zshrc): export PATH=\"$BIN_DIR:\$PATH\"" ;;
esac
"$BIN_DIR/hlab" version 2>/dev/null || true

# Runtime dependencies come after hlab so they can never block the primary binary.
install_deps
summary
