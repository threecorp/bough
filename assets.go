// Package bough embeds the Claude Code artifacts bough installs — the same
// skills/ and commands/ trees the repo publishes as a Claude Code plugin.
//
// The embed lives at the module root on purpose. Those two directories must sit
// at the repo root for Claude Code's plugin auto-discovery to find them, and
// //go:embed cannot reach outside its own package directory ("../skills" is not
// allowed). Embedding from the root is what lets `bough claude skill install`
// (the CLI path, for operators who do not want the plugin) and the plugin ship
// byte-identical content from a single copy — a second copy under internal/
// would drift the moment one side is edited.
package bough

import "embed"

// Assets carries bough's canonical Claude Code artifacts:
//
//	skills/using-bough/SKILL.md   → model-invoked orchestration guidance
//	commands/*.md                 → the /bough:<verb> slash commands
//
// Consumers deploy a subtree with procutil.DeployAssets(bough.Assets, "skills", dst).
//
//go:embed skills commands
var Assets embed.FS
