package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

type logsArgs struct {
	repository string
	help       bool
}

func parseLogsArgs(args []string) (logsArgs, error) {
	var out logsArgs
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" {
			continue
		}
		if helpRequested(arg) {
			out.help = true
			return out, nil
		}
		if strings.HasPrefix(arg, "-") {
			return out, fmt.Errorf("unknown flag %q (usage: %s)", arg, commandUsage("logs"))
		}
		if out.repository != "" {
			return out, fmt.Errorf("unexpected extra argument %q", arg)
		}
		out.repository = arg
	}
	return out, nil
}

// runLogs lists the Markdown transcripts a session wrote into the repository's
// .duo/logs directory. The files are named after the session id, which already
// begins with a sortable timestamp, so grouping by session id and ordering by
// name is the same as ordering by time.
func runLogs(ctx context.Context, args []string) error {
	parsed, err := parseLogsArgs(args)
	if err != nil {
		return err
	}
	if parsed.help {
		printUsage()
		return nil
	}

	repoPath := parsed.repository
	if repoPath == "" {
		repoPath = strings.TrimSpace(os.Getenv("DUO_REPO"))
		if repoPath == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return err
			}
			repoPath = cwd
		}
	}
	if abs, err := filepath.Abs(repoPath); err == nil {
		repoPath = abs
	}
	root, err := workspace.FindRoot(ctx, repoPath)
	if err != nil {
		return err
	}

	dir := filepath.Join(root, ".duo", sessionstore.LogsDirName)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Printf("No session logs yet for %s\nThey appear under %s once a session has run.\n", root, dir)
			return nil
		}
		return err
	}

	// Group by session id so the three files of one session stay together.
	bySession := map[string][]os.DirEntry{}
	var order []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".md")
		for _, suffix := range []string{"-austin", "-tony"} {
			id = strings.TrimSuffix(id, suffix)
		}
		if _, seen := bySession[id]; !seen {
			order = append(order, id)
		}
		bySession[id] = append(bySession[id], e)
	}
	if len(order) == 0 {
		fmt.Printf("No session logs yet for %s\nThey appear under %s once a session has run.\n", root, dir)
		return nil
	}
	sort.Sort(sort.Reverse(sort.StringSlice(order)))

	fmt.Printf("Session logs for %s:\n\n", root)
	total := 0
	for _, id := range order {
		files := bySession[id]
		sort.Slice(files, func(i, j int) bool { return files[i].Name() < files[j].Name() })
		when := time.Time{}
		if info, err := files[0].Info(); err == nil {
			when = info.ModTime()
		}
		stamp := "unknown time"
		if !when.IsZero() {
			stamp = when.Local().Format("2006-01-02 15:04")
		}
		fmt.Printf("  %s  %s  (%d file(s))\n", id, stamp, len(files))
		for _, f := range files {
			size := int64(0)
			if info, err := f.Info(); err == nil {
				size = info.Size()
			}
			fmt.Printf("    %s  %s\n", filepath.Join(dir, f.Name()), humanLogSize(size))
			total++
		}
		fmt.Println()
	}
	fmt.Printf("Total: %d session(s), %d file(s) in %s\n", len(order), total, dir)
	return nil
}

func humanLogSize(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(1024*1024))
	}
}
