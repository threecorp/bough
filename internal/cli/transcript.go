package cli

import (
	"encoding/json"
	"os"
	"strings"
)

// The files a session just read or edited are the strongest statement
// available about what it is working on, and the prompt itself often does
// not contain them: "why is this failing?" names nothing, while the last
// four Read calls name the subsystem exactly. Claude Code passes the
// transcript path on every UserPromptSubmit payload, so the signal is
// there for the cost of reading the tail of one file.
//
// Best-effort throughout: the transcript schema is the host's, not a
// contract with bough, so anything unexpected yields NO signal rather than
// an error. A hook that fails because a log line changed shape has traded
// a small improvement for the whole turn.

// transcriptReader bounds how much of the transcript is read. The limits
// are fields rather than package constants so a test can exercise the
// truncation paths without writing a 256KB fixture.
type transcriptReader struct {
	// maxFiles caps how many paths are returned. Recent means recent: the
	// twentieth-most-recent file is not evidence about this prompt.
	maxFiles int
	// tailBytes is how much of the END of the transcript is read. The file
	// grows for the whole session and only its tail is about now.
	tailBytes int64
	// maxLines bounds the JSON parsing after the tail is read, so a
	// transcript of very short lines cannot make this the slow step.
	maxLines int
}

func newTranscriptReader() transcriptReader {
	return transcriptReader{maxFiles: 10, tailBytes: 256 << 10, maxLines: 400}
}

// fileTools are the tool names whose file_path says "the session is
// looking at this". Deliberately not every tool that takes a path: a Bash
// command mentioning a file is not the same evidence as opening it.
var fileTools = map[string]struct{}{
	"Read": {}, "Edit": {}, "Write": {}, "NotebookEdit": {},
}

// recentFiles returns the file paths the session most recently read or
// edited, newest first, deduplicated.
func (r transcriptReader) recentFiles(path string) []string {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	if off := info.Size() - r.tailBytes; off > 0 {
		if _, err := f.Seek(off, 0); err != nil {
			return nil
		}
	}
	buf := make([]byte, min(info.Size(), r.tailBytes))
	n, _ := f.Read(buf) // a short read is still usable: fewer lines, no error
	lines := strings.Split(string(buf[:n]), "\n")
	if len(lines) > r.maxLines {
		lines = lines[len(lines)-r.maxLines:]
	}

	var out []string
	seen := map[string]struct{}{}
	// Newest first: the reverse walk is what makes "recent" mean recent
	// once maxFiles bites.
	for i := len(lines) - 1; i >= 0; i-- {
		for _, p := range filePathsInTranscriptLine(lines[i]) {
			if _, dup := seen[p]; dup {
				continue
			}
			seen[p] = struct{}{}
			out = append(out, p)
			if len(out) >= r.maxFiles {
				return out
			}
		}
	}
	return out
}

// filePathsInTranscriptLine pulls the file_path out of every file-tool
// call in one transcript line. A line that does not parse, or carries no
// tool call, contributes nothing — which is the normal case, since most
// lines are prose.
func filePathsInTranscriptLine(line string) []string {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	var rec struct {
		Message struct {
			Content []struct {
				Type  string `json:"type"`
				Name  string `json:"name"`
				Input struct {
					FilePath string `json:"file_path"`
				} `json:"input"`
			} `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(line), &rec) != nil {
		return nil
	}
	var out []string
	for _, blk := range rec.Message.Content {
		if blk.Type != "tool_use" {
			continue
		}
		if _, ok := fileTools[blk.Name]; !ok {
			continue
		}
		if blk.Input.FilePath != "" {
			out = append(out, blk.Input.FilePath)
		}
	}
	return out
}
