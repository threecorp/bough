package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/ikeikeikeike/bough/internal/homunculus"
)

// The quarantine's review loop. The gate is deliberately only the net —
// a regex cannot tell "do X" from "never do X", so every rule-shaped
// note trips it, and the reference design treats that as the guard's
// known cost rather than a tuning problem. The judging of MEANING is a
// review, and this command is where the review's conclusion lands as
// data instead of as a hand-typed edit that drifts from the judgement
// that produced it:
//
//	keep   → the note is the rule (or otherwise correct). Its id joins
//	         instinct.gate.allow_ids in .bough.yaml with the reason as a
//	         comment, and the file returns to .staging — the next
//	         observer pass re-adopts it and the gate now reports it as
//	         exempt instead of holding it.
//	retire → a true violation. The file STAYS in quarantine (nothing
//	         here deletes, ever); the reason is appended to the batch
//	         REPORT.md so the next reader inherits the judgement.
//	done   → the batch is reviewed: touch its REVIEWED marker, which is
//	         what silences the per-prompt notice.
//
// Verdicts, not moves: recording the reason next to the decision is the
// contract. An allowlist entry with no "why" is one nobody can audit,
// and a re-quarantined note with no recorded retirement reads as a
// fresh question.
func newInstinctVerdictCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "verdict",
		Short: "Record a review verdict on a quarantined instinct: keep, retire, or done",
	}
	cmd.AddCommand(newVerdictKeepCmd(), newVerdictRetireCmd(), newVerdictDoneCmd())
	return cmd
}

func newVerdictKeepCmd() *cobra.Command {
	var root, batch, why string
	cmd := &cobra.Command{
		Use:   "keep <id>",
		Args:  cobra.ExactArgs(1),
		Short: "The note is the rule (or correct): allowlist it with the reason, restore it to .staging",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerdictKeep(cmd.OutOrStdout(), cmd, root, batch, args[0], why)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&batch, "batch", "", "quarantine batch dir (default: search every unreviewed batch, newest first)")
	cmd.Flags().StringVar(&why, "why", "", "why this note is correct, citing the governance it enforces (required)")
	_ = cmd.MarkFlagRequired("why")
	return cmd
}

func newVerdictRetireCmd() *cobra.Command {
	var root, batch, why string
	cmd := &cobra.Command{
		Use:   "retire <id>",
		Args:  cobra.ExactArgs(1),
		Short: "A true violation: record why in the batch REPORT.md; the file stays quarantined",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVerdictRetire(cmd.OutOrStdout(), root, batch, args[0], why)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&batch, "batch", "", "quarantine batch dir (default: search every unreviewed batch, newest first)")
	cmd.Flags().StringVar(&why, "why", "", "what the note teaches, and which rule that breaks (required)")
	_ = cmd.MarkFlagRequired("why")
	return cmd
}

func newVerdictDoneCmd() *cobra.Command {
	var root, batch string
	cmd := &cobra.Command{
		Use:   "done",
		Short: "Mark a batch REVIEWED, silencing its per-prompt notice",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runVerdictDone(cmd.OutOrStdout(), root, batch)
		},
	}
	cmd.Flags().StringVar(&root, "root", "", "monorepo root (default: $PWD)")
	cmd.Flags().StringVar(&batch, "batch", "", "quarantine batch dir (default: the newest)")
	return cmd
}

// verdictEnv resolves the project the verdict applies to.
func verdictEnv(root string) (homunculus.ProjectIdentity, homunculus.Layout, error) {
	cwd := root
	if cwd == "" {
		w, err := os.Getwd()
		if err != nil {
			return homunculus.ProjectIdentity{}, homunculus.Layout{}, fmt.Errorf("verdict: getwd: %w", err)
		}
		cwd = w
	}
	ident, err := homunculus.DetectIdentity(cwd)
	if err != nil {
		return homunculus.ProjectIdentity{}, homunculus.Layout{}, err
	}
	return ident, homunculus.NewLayout(), nil
}

// quarantineBatches lists batch dirs newest first. The reference resolves
// only the latest batch; searching every unreviewed one is this port's
// concession to reality — its operator reviews a backlog, and "the id you
// named is in batch 14 of 42" is not an error worth typing --batch for.
func quarantineBatches(qroot string) ([]string, error) {
	entries, err := os.ReadDir(qroot)
	if err != nil {
		return nil, fmt.Errorf("verdict: no quarantine at %s: %w", qroot, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, filepath.Join(qroot, e.Name()))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	return dirs, nil
}

// findHeldFile locates <id>.md in the given batch, falling back to a
// frontmatter scan — filename and id diverge in this corpus, and the
// allowlist keys on id.
func findHeldFile(batch, id string) string {
	direct := filepath.Join(batch, id+".md")
	if _, err := os.Stat(direct); err == nil {
		return direct
	}
	entries, err := os.ReadDir(batch)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^id:\s*['"]?` + regexp.QuoteMeta(id) + `['"]?\s*$`)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" || e.Name() == "REPORT.md" {
			continue
		}
		path := filepath.Join(batch, e.Name())
		raw, rerr := os.ReadFile(path)
		if rerr != nil {
			continue
		}
		if re.Match(raw) {
			return path
		}
	}
	return ""
}

// resolveHeld finds the quarantined file for id: the named batch when
// given, every batch newest-first when not.
func resolveHeld(qroot, batch, id string) (string, string, error) {
	if batch != "" {
		if f := findHeldFile(batch, id); f != "" {
			return batch, f, nil
		}
		return "", "", fmt.Errorf("verdict: %s not found in %s", id, batch)
	}
	batches, err := quarantineBatches(qroot)
	if err != nil {
		return "", "", err
	}
	for _, b := range batches {
		if f := findHeldFile(b, id); f != "" {
			return b, f, nil
		}
	}
	return "", "", fmt.Errorf("verdict: %s not found in any batch under %s", id, qroot)
}

func runVerdictKeep(out io.Writer, cmd *cobra.Command, root, batch, id, why string) error {
	ident, layout, err := verdictEnv(root)
	if err != nil {
		return err
	}
	b, held, err := resolveHeld(layout.QuarantineDir(ident.ID), batch, id)
	if err != nil {
		return err
	}
	// The allowlist entry lands FIRST: if the move then fails the worst
	// state is an allowlisted id still in quarantine, which the next
	// keep retries. The other order re-stages the note un-allowlisted,
	// and the next pass re-quarantines it — undoing the verdict.
	cfgPath := resolveConfigPath(cmd, ident.Root)
	if err := appendAllowID(cfgPath, id, why); err != nil {
		return err
	}
	staging := layout.StagingDir(ident.ID)
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return fmt.Errorf("verdict keep: mkdir staging: %w", err)
	}
	dst := filepath.Join(staging, filepath.Base(held))
	if err := os.Rename(held, dst); err != nil {
		return fmt.Errorf("verdict keep: restore %s: %w", held, err)
	}
	fmt.Fprintf(out, "keep %s\n  allow_ids += %s (%s)\n  restored %s → %s (the next observer pass re-adopts it)\n",
		id, id, cfgPath, held, dst)
	_ = b
	return nil
}

func runVerdictRetire(out io.Writer, root, batch, id, why string) error {
	ident, layout, err := verdictEnv(root)
	if err != nil {
		return err
	}
	b, held, err := resolveHeld(layout.QuarantineDir(ident.ID), batch, id)
	if err != nil {
		return err
	}
	report := filepath.Join(b, "REPORT.md")
	f, err := os.OpenFile(report, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("verdict retire: open %s: %w", report, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n## retired: %s\n- why: %s\n", id, why); err != nil {
		return fmt.Errorf("verdict retire: append %s: %w", report, err)
	}
	fmt.Fprintf(out, "retire %s\n  reason appended to %s; the file stays at %s (nothing is deleted)\n", id, report, held)
	return nil
}

func runVerdictDone(out io.Writer, root, batch string) error {
	ident, layout, err := verdictEnv(root)
	if err != nil {
		return err
	}
	b := batch
	if b == "" {
		batches, berr := quarantineBatches(layout.QuarantineDir(ident.ID))
		if berr != nil {
			return berr
		}
		if len(batches) == 0 {
			return fmt.Errorf("verdict done: no batches under %s", layout.QuarantineDir(ident.ID))
		}
		b = batches[0]
	}
	marker := filepath.Join(b, "REVIEWED")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		return fmt.Errorf("verdict done: %w", err)
	}
	fmt.Fprintf(out, "done — %s\n  the per-prompt notice no longer counts this batch\n", marker)
	return nil
}

// appendAllowID appends id to instinct.gate.allow_ids in the .bough.yaml
// at cfgPath, with why as the entry's line comment. The file is the
// operator's — every other key, and every comment, must survive the
// round-trip verbatim, which is why this edits the yaml.Node tree rather
// than re-marshalling through the typed config (that would drop every
// comment in the file).
//
// A missing config REFUSES rather than minting one: this is a manual
// write path into policy, and "the file was not there so I created it"
// is not a decision the operator made.
func appendAllowID(cfgPath, id, why string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return fmt.Errorf("verdict keep: the allowlist lives in the config, and it could not be read (create one first): %w", err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("verdict keep: %s does not parse: %w", cfgPath, err)
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 {
		return fmt.Errorf("verdict keep: %s is empty — add a schema_version first", cfgPath)
	}
	rootMap := doc.Content[0]
	gate := ensureMapChild(ensureMapChild(rootMap, "instinct"), "gate")
	allow := mapChild(gate, "allow_ids")
	if allow == nil {
		key := &yaml.Node{Kind: yaml.ScalarNode, Value: "allow_ids"}
		allow = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		gate.Content = append(gate.Content, key, allow)
	}
	for _, existing := range allow.Content {
		if existing.Value == id {
			return fmt.Errorf("verdict keep: %s is already in allow_ids (%s)", id, cfgPath)
		}
	}
	allow.Content = append(allow.Content, &yaml.Node{
		Kind:        yaml.ScalarNode,
		Value:       id,
		LineComment: "# " + why,
	})
	var b strings.Builder
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return fmt.Errorf("verdict keep: re-encode %s: %w", cfgPath, err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("verdict keep: re-encode %s: %w", cfgPath, err)
	}
	tmp := cfgPath + ".bough.tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return fmt.Errorf("verdict keep: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, cfgPath); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("verdict keep: rename %s: %w", tmp, err)
	}
	return nil
}

// mapChild returns the value node for key in a mapping node, or nil.
func mapChild(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// ensureMapChild returns the mapping node for key, appending an empty
// one when absent.
func ensureMapChild(m *yaml.Node, key string) *yaml.Node {
	if child := mapChild(m, key); child != nil {
		return child
	}
	k := &yaml.Node{Kind: yaml.ScalarNode, Value: key}
	v := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	m.Content = append(m.Content, k, v)
	return v
}
