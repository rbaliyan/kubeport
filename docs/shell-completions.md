# Shell Completions

Shell completion scripts are included in release archives and the Homebrew formula.

## Bash

```bash
# Source directly
source <path-to>/completions/kubeport.bash

# Or install system-wide
sudo cp completions/kubeport.bash /etc/bash_completion.d/kubeport
```

## Zsh

```bash
# Source directly
source <path-to>/completions/kubeport.zsh

# Or add to your completions directory
mkdir -p ~/.zsh/completions
cp completions/kubeport.zsh ~/.zsh/completions/_kubeport
```

Make sure your `~/.zshrc` includes the completions directory in `fpath`:

```bash
fpath=(~/.zsh/completions $fpath)
autoload -Uz compinit && compinit
```

## Fish

```bash
# Source directly
source <path-to>/completions/kubeport.fish

# Or install to Fish completions directory
cp completions/kubeport.fish ~/.config/fish/completions/kubeport.fish
```

## Homebrew

If you installed via Homebrew, completions are set up automatically.
