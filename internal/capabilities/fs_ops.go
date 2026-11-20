package capabilities

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/aliothmoon/agy-ptc/internal/sandbox"
)

const maxCopyFiles = 5000

// applyEOL picks line endings for written text. eol: "lf" | "crlf" | "" (keep the existing file's style;
// new .bat/.cmd files get CRLF because cmd.exe misparses LF-only batch files).
func applyEOL(path string, orig []byte, existed bool, next []byte, eol string) ([]byte, error) {
	switch strings.ToLower(eol) {
	case "lf":
		return lf(next), nil
	case "crlf":
		return bytes.ReplaceAll(lf(next), []byte("\n"), []byte("\r\n")), nil
	case "", "keep", "preserve":
		if ext := strings.ToLower(filepath.Ext(path)); ext == ".bat" || ext == ".cmd" {
			return bytes.ReplaceAll(lf(next), []byte("\n"), []byte("\r\n")), nil
		}
		if existed {
			return keepLineEndings(orig, next), nil
		}
		return next, nil
	}
	return nil, sandbox.NewHostError(sandbox.KindBadArgs, nil, "eol must be \"lf\" or \"crlf\" (got %q)", eol)
}

// fileOps returns fs.mkdir / fs.rm / fs.rename / fs.cp (Node names; `mv` and `copy` are aliases in the bootstrap).
// They act immediately (not transactional); use t.write / t.rm inside a tx for rollback-able changes.
func (b *FSSearchBinder) fileOps(env *sandbox.ExecutionEnv) map[string]sandbox.AsyncHostFunc {
	return map[string]sandbox.AsyncHostFunc{
		// fs.mkdir(path): creates parents too (like mkdir -p); no error if it exists.
		"mkdir": func(_ context.Context, args []any) (any, error) {
			p, err := requireStringArg(args, 0, "path")
			if err != nil {
				return nil, err
			}
			abs, err := b.Policy.ResolveAndCheckWrite(env.Cwd, p)
			if err != nil {
				return nil, err
			}
			return nil, os.MkdirAll(abs, 0o755)
		},
		// fs.rm(path, { force? }): removes a file, symlink or empty dir. Non-empty dirs need approval (recursive delete).
		"rm": func(_ context.Context, args []any) (any, error) {
			p, err := requireStringArg(args, 0, "path")
			if err != nil {
				return nil, err
			}
			opts := optsAt(args, 1)
			abs, err := b.Policy.ResolveAndCheckWrite(env.Cwd, p)
			if err != nil {
				return nil, err
			}
			info, err := os.Lstat(abs)
			if err != nil {
				if errors.Is(err, fs.ErrNotExist) && optBool(opts, "force", false) {
					return nil, nil
				}
				return nil, err
			}
			if info.IsDir() {
				if entries, _ := os.ReadDir(abs); len(entries) > 0 {
					return nil, sandbox.NewHostError(sandbox.KindNeedsApproval, nil, "%s is a non-empty dir — recursive delete; run it via native run_command", p)
				}
			}
			return nil, os.Remove(abs)
		},
		// fs.rename(from, to): moves/renames a file or dir, creating the target's parent; overwrites files like Node.
		"rename": func(_ context.Context, args []any) (any, error) {
			from, err := requireStringArg(args, 0, "from")
			if err != nil {
				return nil, err
			}
			to, err := requireStringArg(args, 1, "to")
			if err != nil {
				return nil, err
			}
			absFrom, err := b.Policy.ResolveAndCheckWrite(env.Cwd, from)
			if err != nil {
				return nil, err
			}
			absTo, err := b.Policy.ResolveAndCheckWrite(env.Cwd, to)
			if err != nil {
				return nil, err
			}
			if _, err := os.Lstat(absFrom); err != nil {
				return nil, err
			}
			if err := os.MkdirAll(filepath.Dir(absTo), 0o755); err != nil {
				return nil, err
			}
			return nil, os.Rename(absFrom, absTo)
		},
		// fs.cp(from, to, { recursive? }): copies a file, or a directory tree with { recursive: true } (like Node fs.cp).
		"cp": func(_ context.Context, args []any) (any, error) {
			from, err := requireStringArg(args, 0, "from")
			if err != nil {
				return nil, err
			}
			to, err := requireStringArg(args, 1, "to")
			if err != nil {
				return nil, err
			}
			opts := optsAt(args, 2)
			absFrom, err := b.Policy.ResolveAndCheckRead(env.Cwd, from)
			if err != nil {
				return nil, err
			}
			info, err := os.Stat(absFrom)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() {
				absTo, err := b.Policy.ResolveAndCheckWrite(env.Cwd, to)
				if err != nil {
					return nil, err
				}
				return nil, copyFile(absFrom, absTo)
			}
			if !optBool(opts, "recursive", false) {
				return nil, sandbox.NewHostError(sandbox.KindBadArgs, nil, "%s is a directory; pass { recursive: true }", from)
			}
			var files []string
			walkErr := filepath.WalkDir(absFrom, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					files = append(files, p)
					if len(files) > maxCopyFiles {
						return sandbox.NewHostError(sandbox.KindBadArgs, nil, "%s has more than %d files; use sh.run", from, maxCopyFiles)
					}
				}
				return nil
			})
			if walkErr != nil {
				return nil, walkErr
			}
			for _, src := range files {
				rel, _ := filepath.Rel(absFrom, src)
				dst, err := b.Policy.ResolveAndCheckWrite(env.Cwd, filepath.Join(to, rel))
				if err != nil {
					return nil, err
				}
				if err := copyFile(src, dst); err != nil {
					return nil, err
				}
			}
			return nil, nil
		},
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func optsAt(args []any, i int) map[string]any {
	if len(args) > i {
		if m, ok := args[i].(map[string]any); ok {
			return m
		}
	}
	return nil
}
