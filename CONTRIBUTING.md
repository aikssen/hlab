# Contributing to hlab

Thanks for your interest in hlab! This guide covers how to build, test, and submit
changes.

## What hlab is

hlab is a Go **CLI and full-screen TUI** that creates and manages Proxmox VMs and LXC
containers. It **discovers** infrastructure through the Proxmox API and **orchestrates
the official tools** — Terraform for the guest lifecycle, Ansible for provisioning — so
you never hand-write Terraform or Ansible. Two-phase model:

- `hlab vm create` / `hlab ct create` → **lifecycle** (Terraform: clone/create + cloud-init).
- `hlab vm provision` / `hlab ct provision` → **configuration** (Ansible: software + dotfiles).

Layers stay separate: Terraform = lifecycle · Ansible = provisioning · hlab = tooling.

## Prerequisites

- **Go** (see `go.mod` for the version) and **git**.
- **Terraform** and **ansible-core** for running hlab against a real Proxmox cluster
  (Ansible only for `provision`). The easiest setup is [mise](https://mise.jdx.dev):

  ```bash
  mise install          # Go + Terraform
  mise use -g pipx:ansible-core
  ```

- A Proxmox VE cluster + an API token for end-to-end testing (see
  [docs/proxmox-token.md](docs/proxmox-token.md)). Unit tests need none of this.

## Build & test

```bash
go build ./...              # build
go vet ./...                # static checks
gofmt -l .                  # must print nothing (formatting)
go test -race ./...         # unit tests
./scripts/install.sh        # build + install to ~/.local/bin (idempotent)
```

CI runs gofmt + build + vet + `go test -race` on every push and PR; please make sure
these pass locally first.

### What is unit-tested

Unit tests cover the pure, logic-heavy code (no live Proxmox/Terraform/Ansible or
network) and live as `*_test.go` next to the code. The process-invoking layers (Proxmox
HTTP, `terraform`/`ansible` exec, the bubbletea `Update`/`View`, command glue) are
validated end-to-end against a real (or throwaway) guest rather than in unit tests — they
intentionally lack DI seams. New pure logic should come with tests.

## Adding software to the catalog

hlab installs optional software during provisioning from an embedded catalog:

1. Add an entry to `assets/additional-software.yaml` (`key`, `label`, `mise`).
2. Add `assets/ansible/tasks/<key>.yml` (idempotent: check-then-install).
3. Wire an `include_tasks` in `assets/ansible/playbook.yml` gated by
   `when: "'<key>' in software"`.
4. Rebuild (the assets are embedded via `//go:embed`).

Runtimes use the existing mise tasks. CLI installers (`curl | bash`) run as the target
user — check both `command -v` and the tool's actual install path.

## Submitting changes

- Branch off `main`, keep changes focused, and open a pull request.
- Write clear commit messages; conventional prefixes (`feat:`, `fix:`, `docs:`,
  `refactor:`, `test:`) are appreciated but not required.
- Update `CHANGELOG.md` under an `## [Unreleased]` section for user-visible changes, and
  the relevant `docs/` page when behavior changes.
- Match the surrounding code style; comments explain constraints, not narration.
- Be respectful and constructive in reviews and discussions.

## Reporting issues

Open a GitHub issue with what you expected, what happened, your OS/arch, the hlab version
(`hlab version`), and — for provisioning or Proxmox problems — the relevant output run
with `-v` (or `-vv`). Never paste API tokens, passwords, or private keys.

## License

By contributing, you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
