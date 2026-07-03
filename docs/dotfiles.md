# Dotfiles

Your dotfiles are just another catalog entry (`dotfiles`): selecting it clones the
repo you configured and runs its `bootstrap.sh` (server profile) during
provisioning. The option is **hidden until you configure a repo** — there is no
built-in default:

```bash
hlab setup --dotfiles-repo git@github.com:you/dotfiles.git
```

(or set `dotfiles_repo` in the setup wizard / TUI setup form). Once set, `dotfiles`
appears in the provisioning checklist and works with `--software dotfiles`.

## Private repos and SSH agent forwarding

If your dotfiles repository is **private**, hlab clones it on the guest using your
**forwarded SSH agent** — your private key never leaves your machine. Before
provisioning with dotfiles, load the key that authorizes to your git host:

```bash
ssh-add ~/.ssh/id_ed25519     # the key that authenticates to GitHub
ssh-add -l                    # verify it is loaded
```

hlab passes `ForwardAgent=yes` to Ansible's SSH connection and clones via the
`dotfiles_repo` SSH URL. `bootstrap.sh` runs as the connection user
(`become: false`) so the forwarded agent is available to clone. If `dotfiles` is
selected but the agent has no keys, hlab warns before provisioning.
