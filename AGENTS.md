# Agents instructions

## Modifications

For any modifications in this repo, ask the user whether to edit on the main branch without commiting or use git worktrees.
If using git worktrees:

- use the appropriate "using-git-worktrees" skill and place them in the .worktrees directory.
- create a PR using gh CLI when finished.

## Deployed apps

Always check ./argocd-apps/applicationset.yaml for the list of currently deployed apps.

## Superpowers

Never git add things in docs/superpowers forcefully.

## Golang

When running go build, always delete the generated binary afterwards.
